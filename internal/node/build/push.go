package build

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/docker"
)

// checkPushTarget rejects a push target that can't work before anything is
// built.
func checkPushTarget(p *node.PushTarget) error {
	named, err := reference.ParseNormalizedNamed(p.Ref)
	if err != nil {
		return fmt.Errorf("push target %q: %w", p.Ref, err)
	}
	if _, ok := named.(reference.Digested); ok {
		return fmt.Errorf("push target %q must be a tag, not a digest", p.Ref)
	}
	if p.Token == "" {
		return fmt.Errorf("push target %q has no registry token", p.Ref)
	}
	return nil
}

// dockerPush pushes the built image (tag, in the local Docker) to p.Ref
// and logs the pushed manifest's digest and how long the push took.
//
// It is the push for a builder without the relay (relayHost): BuildKit
// can't reach the registry on the host's loopback then, Docker can. It
// uses plain HTTP for loopback registries without extra config and sends
// the token as is (docker.RegistryAuth). The layers stream from Docker's
// store to the registry; nothing passes through the daemon. p.Ref is
// only a temporary second name for the image.
func (b *Builder) dockerPush(ctx context.Context, tag string, p *node.PushTarget, logs io.Writer) error {
	if _, err := b.docker.ImageTag(ctx, client.ImageTagOptions{Source: tag, Target: p.Ref}); err != nil {
		return fmt.Errorf("tag %s: %w", p.Ref, err)
	}
	defer func() {
		_, _ = b.docker.ImageRemove(context.WithoutCancel(ctx), p.Ref, client.ImageRemoveOptions{})
	}()
	_, _ = fmt.Fprintf(logs, "Pushing %s\n", p.Ref)
	started := time.Now()
	resp, err := b.docker.ImagePush(ctx, p.Ref, client.ImagePushOptions{RegistryAuth: docker.RegistryAuth(p.Token)})
	if err != nil {
		return fmt.Errorf("push %s: %w", p.Ref, err)
	}
	defer func() { _ = resp.Close() }()
	var digest string
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("push %s: %w", p.Ref, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("push %s: %s", p.Ref, msg.Error.Message)
		}
		if d, ok := pushedDigest(msg); ok {
			digest = d
		}
	}
	if digest == "" {
		return fmt.Errorf("push %s: docker reported no digest", p.Ref)
	}
	_, _ = fmt.Fprintf(logs, "Pushed %s (%s) in %.1fs\n", p.Ref, digest, time.Since(started).Seconds())
	return nil
}

// pull fetches the image BuildKit pushed (p.Ref) into the local Docker
// and names it tag, which is what a build without a push loads: the
// layers Docker already has (an earlier release's) stay, only new ones
// come over loopback. p.Ref is only a temporary second name, as in
// dockerPush.
func (b *Builder) pull(ctx context.Context, tag string, p *node.PushTarget, logs io.Writer) error {
	started := time.Now()
	resp, err := b.docker.ImagePull(ctx, p.Ref, client.ImagePullOptions{RegistryAuth: docker.RegistryAuth(p.Token)})
	if err != nil {
		return fmt.Errorf("pull %s: %w", p.Ref, err)
	}
	defer func() { _ = resp.Close() }()
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("pull %s: %w", p.Ref, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("pull %s: %s", p.Ref, msg.Error.Message)
		}
	}
	if _, err := b.docker.ImageTag(ctx, client.ImageTagOptions{Source: p.Ref, Target: tag}); err != nil {
		return fmt.Errorf("tag %s: %w", tag, err)
	}
	_, _ = b.docker.ImageRemove(context.WithoutCancel(ctx), p.Ref, client.ImageRemoveOptions{})
	_, _ = fmt.Fprintf(logs, "Loaded into Docker in %.1fs\n", time.Since(started).Seconds())
	return nil
}

// registryAuth gives BuildKit the push token for the one registry host
// it pushes to, through the build's session. BuildKit asks the session
// for a token directly only when it names a token authority (a key it
// can check the session with), so registryAuth names a fresh one per
// build. Other registries (base images) get neither and are pulled
// anonymously, as without it.
type registryAuth struct {
	auth.UnimplementedAuthServer
	host, token string
	key         ed25519.PrivateKey
}

func newRegistryAuth(host, token string) *registryAuth {
	_, key, _ := ed25519.GenerateKey(nil)
	return &registryAuth{host: host, token: token, key: key}
}

func (a *registryAuth) Register(s *grpc.Server) { auth.RegisterAuthServer(s, a) }

// Credentials answers every host with none: anonymous pulls.
func (a *registryAuth) Credentials(context.Context, *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	return &auth.CredentialsResponse{}, nil
}

func (a *registryAuth) GetTokenAuthority(_ context.Context, req *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	if req.Host != a.host {
		return nil, status.Error(codes.Unimplemented, "no token authority for "+req.Host)
	}
	return &auth.GetTokenAuthorityResponse{PublicKey: a.key.Public().(ed25519.PublicKey)}, nil
}

func (a *registryAuth) FetchToken(_ context.Context, req *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	if req.Host != a.host {
		return nil, status.Error(codes.PermissionDenied, "no token for "+req.Host)
	}
	return &auth.FetchTokenResponse{Token: a.token}, nil
}

// pushedDigest reads the pushed manifest's digest from a push progress
// message: the "<tag>: digest: sha256:... size: N" status line, or the
// aux record Docker's classic image store sends with it.
func pushedDigest(msg jsonstream.Message) (string, bool) {
	if msg.Aux != nil {
		var aux struct{ Digest string }
		if json.Unmarshal(*msg.Aux, &aux) == nil && aux.Digest != "" {
			return aux.Digest, true
		}
	}
	if _, rest, ok := strings.Cut(msg.Status, ": digest: "); ok {
		if d, _, _ := strings.Cut(rest, " "); strings.HasPrefix(d, "sha256:") {
			return d, true
		}
	}
	return "", false
}
