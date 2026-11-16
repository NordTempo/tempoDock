package core

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/distribution/reference"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Registry credentials (ADR 0051): how an org signs in to private image
// registries. A deploy of an image from a registry host with a credential
// pulls with it (registryPull). Only unlimited admins manage them; the
// password or token is sealed and never leaves core again except to the
// pulling server.

// maxRegistryUsername and maxRegistryPassword bound what a credential
// stores. Tokens are short, but some registries take a JSON key as the
// password (Google's _json_key), a few KB.
const (
	maxRegistryUsername = 256
	maxRegistryPassword = 16 << 10
)

// dockerHub is the host credentials for Docker Hub are stored under.
const dockerHub = "docker.io"

// normalizeRegistryHost lower-cases a registry host and maps Docker Hub's
// other names to docker.io, so one credential covers every way of naming
// it.
func normalizeRegistryHost(host string) string {
	host = strings.ToLower(host)
	switch host {
	case "index.docker.io", "registry-1.docker.io":
		return dockerHub
	}
	return host
}

// imageRegistry is the registry host Docker pulls ref from, normalized:
// docker.io for a reference without one (nginx, acme/app). "" when ref
// isn't a valid reference (its pull fails anyway).
func imageRegistry(ref string) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ""
	}
	return normalizeRegistryHost(reference.Domain(named))
}

// parseRegistryHost checks a registry host as a user typed it (ghcr.io,
// registry.example.com:5000, docker.io) and returns it normalized: what
// Docker reads as the registry of an image under it, no scheme or path.
func parseRegistryHost(in, own string) (string, error) {
	host := normalizeRegistryHost(strings.TrimSpace(in))
	bad := func() error {
		return invalid("registry host %q is not valid; give the host as in an image reference, like ghcr.io or registry.example.com:5000, without https:// or a path", in)
	}
	if host == "" || strings.Contains(host, "://") {
		return "", bad()
	}
	if imageRegistry(host+"/x") != host {
		return "", bad()
	}
	if own != "" && strings.EqualFold(host, own) {
		return "", invalid("%s is Dock's own registry, which Dock signs in to with its own tokens; it needs no credentials", host)
	}
	return host, nil
}

// ListRegistryCredentials returns the org's registry credentials by host,
// without their secrets.
func (c *Core) ListRegistryCredentials(ctx context.Context, a Actor) ([]apitypes.RegistryCredential, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "listing registry credentials"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListRegistryCredentials(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.RegistryCredential, len(rows))
	for i, r := range rows {
		out[i] = toRegistryCredential(r)
	}
	return out, nil
}

// SetRegistryCredential adds or replaces the org's credential for a
// registry host. Deploys pulling from the host use it from their next
// pull; nothing is redeployed.
//
// Not stopped by a locked environment (ADR 0044): credentials are the
// org's and change no environment by themselves. Only a deploy pulls
// with them, and a deploy into a locked environment is refused (or waits
// for approval) on its own. The same holds for DeleteRegistryCredential.
func (c *Core) SetRegistryCredential(ctx context.Context, a Actor, host string, req apitypes.SetRegistryCredentialRequest) (apitypes.RegistryCredential, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "setting registry credentials"); err != nil {
		return apitypes.RegistryCredential{}, err
	}
	host, err := parseRegistryHost(host, c.registryHost())
	if err != nil {
		return apitypes.RegistryCredential{}, err
	}
	user := strings.TrimSpace(req.Username)
	switch {
	case user == "":
		return apitypes.RegistryCredential{}, invalid("the username is required; for a token-only registry, use the name its docs give (often your account name)")
	case len(user) > maxRegistryUsername || strings.IndexFunc(user, unicode.IsControl) >= 0:
		return apitypes.RegistryCredential{}, invalid("the username must be one line of at most %d bytes", maxRegistryUsername)
	case req.Password == "":
		return apitypes.RegistryCredential{}, invalid("the password or token is required")
	case len(req.Password) > maxRegistryPassword:
		return apitypes.RegistryCredential{}, invalid("the password or token is longer than %d KB", maxRegistryPassword>>10)
	}
	sealed, err := c.box.Seal([]byte(req.Password), registrySecretAAD(a.OrgID, host))
	if err != nil {
		return apitypes.RegistryCredential{}, err
	}
	n := now()
	row, err := c.store.UpsertRegistryCredential(ctx, store.UpsertRegistryCredentialParams{
		OrgID: a.OrgID, Host: host, Username: user, Secret: sealed,
		CreatedAt: n, CreatedBy: a.Name, UpdatedAt: n, UpdatedBy: a.Name,
	})
	if err != nil {
		return apitypes.RegistryCredential{}, err
	}
	c.audit(ctx, a, "org.registry_credential_set", nil, nil, host, fmt.Sprintf("set the credentials for registry %s (user %s)", host, user))
	return toRegistryCredential(row), nil
}

// DeleteRegistryCredential removes the org's credential for a registry
// host; pulls from it are anonymous again.
func (c *Core) DeleteRegistryCredential(ctx context.Context, a Actor, host string) error {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "removing registry credentials"); err != nil {
		return err
	}
	host = normalizeRegistryHost(strings.TrimSpace(host))
	n, err := c.store.DeleteRegistryCredential(ctx, store.DeleteRegistryCredentialParams{OrgID: a.OrgID, Host: host})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("no credentials for registry %s; `dock registry credentials` lists them", host)
	}
	c.audit(ctx, a, "org.registry_credential_remove", nil, nil, host, "removed the credentials for registry "+host)
	return nil
}

// registryLogin returns the pull options for an image outside Dock's own
// registry, and the image's registry host: the org's credential for that
// host, or empty options (no Username) to pull anonymously. One query.
// Every such pull asks it first, so it also refuses a registry servers
// may not pull from (checkRegistry).
func (c *Core) registryLogin(ctx context.Context, orgID int64, ref string) (node.PullOptions, string, error) {
	host := imageRegistry(ref)
	if host == "" {
		return node.PullOptions{}, "", nil
	}
	if err := c.checkRegistry(ctx, ref, host); err != nil {
		return node.PullOptions{}, host, err
	}
	row, err := c.store.GetRegistryCredential(ctx, store.GetRegistryCredentialParams{OrgID: orgID, Host: host})
	if store.IsNotFound(err) {
		return node.PullOptions{}, host, nil
	}
	if err != nil {
		return node.PullOptions{}, host, err
	}
	secret, err := c.box.Open(row.Secret, registrySecretAAD(orgID, host))
	if err != nil {
		return node.PullOptions{}, host, fmt.Errorf("open the credentials for registry %s: %w", host, err)
	}
	return node.PullOptions{Username: row.Username, Password: string(secret)}, host, nil
}

// pullRefused guesses from a pull's error message whether the registry
// refused access (no or wrong credentials), to say how to fix it: the
// deploy's error and the diagnosis's pull_failed rule. Docker passes
// registries' messages on as text: "unauthorized", "denied: ...",
// "pull access denied", "authentication required", "forbidden", and
// newer Dockers' "no basic auth credentials" for an anonymous pull.
// Status codes alone aren't matched: digests in messages contain any
// digits.
func pullRefused(msg string) bool {
	msg = strings.ToLower(msg)
	for _, s := range []string{"unauthorized", "denied", "authentication required", "forbidden", "no basic auth credentials"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func toRegistryCredential(r store.RegistryCredential) apitypes.RegistryCredential {
	return apitypes.RegistryCredential{
		Host: r.Host, Username: r.Username,
		CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy,
	}
}
