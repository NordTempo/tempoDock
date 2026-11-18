package core

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/access"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Access protection (ADR 0030): a service's domains can ask visitors for
// a password (HTTP basic auth, checked by Caddy) or for a Dock login
// (checked by the server's access check, internal/node/caddy). A login
// works like this: the access check sends a visitor without a session to
// Dock's /access page; the page, signed in, asks GrantAccess for a
// one-minute grant and sends the browser to the domain's callback, which
// trades it for a session cookie on that hostname.

// accessBcryptCost is lower than bcrypt.DefaultCost on purpose: Caddy
// checks the password on requests from new visitors and on every wrong
// guess, on the server the apps share, and remembers only right ones. At
// cost 6 a check takes about 3 ms; a 12-character password stays out of
// reach of online guessing either way.
const accessBcryptCost = 6

// minAccessPassword is the shortest password a service accepts, as for
// user accounts.
const minAccessPassword = 12

// ServiceAccess returns who can open a service's domains.
func (c *Core) ServiceAccess(ctx context.Context, a Actor, ref ServiceRef) (apitypes.Access, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading who can open a service")
	if err != nil {
		return apitypes.Access{}, err
	}
	row, err := c.store.GetServiceAccess(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return apitypes.Access{Mode: apitypes.AccessPublic}, nil
	}
	if err != nil {
		return apitypes.Access{}, err
	}
	return toAccess(row), nil
}

func toAccess(r store.ServiceAccess) apitypes.Access {
	return apitypes.Access{Mode: apitypes.AccessMode(r.Mode), Username: r.Username, UpdatedBy: r.UpdatedBy, UpdatedAt: &r.UpdatedAt}
}

// SetServiceAccess changes who can open a service's domains and reroutes
// its server at once. Turning on a Dock login keeps the service's key if
// it already had one, so signed-in visitors stay signed in; switching to
// another mode and back signs everyone out.
func (c *Core) SetServiceAccess(ctx context.Context, a Actor, ref ServiceRef, req apitypes.SetAccessRequest) (apitypes.Access, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "changing who can open a service")
	if err != nil {
		return apitypes.Access{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.Access{}, invalid("service %s is archived", ss)
	}
	cur, err := c.store.GetServiceAccess(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		cur, err = store.ServiceAccess{}, nil
	}
	if err != nil {
		return apitypes.Access{}, err
	}
	row := store.ServiceAccess{ServiceID: ss.svc.ID, Mode: string(req.Mode), UpdatedBy: a.Name, UpdatedAt: now()}
	var summary string
	switch req.Mode {
	case apitypes.AccessPublic:
		if cur.Mode == "" {
			return apitypes.Access{Mode: apitypes.AccessPublic}, nil
		}
		if err := c.store.DeleteServiceAccess(ctx, ss.svc.ID); err != nil {
			return apitypes.Access{}, err
		}
		summary = "opened its domains to everyone"
	case apitypes.AccessPassword:
		if row.Username, err = accessUsername(req.Username); err != nil {
			return apitypes.Access{}, err
		}
		if row.PasswordHash, err = accessPasswordHash(req.Password, cur); err != nil {
			return apitypes.Access{}, err
		}
		summary = "protected its domains with a password for " + row.Username
	case apitypes.AccessLogin:
		if err := c.loginSupported(ss.env.ServerID); err != nil {
			return apitypes.Access{}, err
		}
		row.Key = cur.Key
		if cur.Mode != string(apitypes.AccessLogin) {
			if row.Key, err = c.box.Seal(access.NewKey(), accessKeyAAD(ss.svc.ID)); err != nil {
				return apitypes.Access{}, err
			}
		}
		summary = "protected its domains with a Dock login"
	default:
		return apitypes.Access{}, invalid("access mode %q is not one of public, password or login", req.Mode)
	}
	if req.Mode != apitypes.AccessPublic {
		if err := c.store.SetServiceAccess(ctx, store.SetServiceAccessParams(row)); err != nil {
			return apitypes.Access{}, err
		}
	}
	if err := c.applyRoutes(ctx, ss.env.ServerID); err != nil {
		c.log.Error("apply routes after changing access", "service", ss.String(), "err", err)
	}
	c.audit(ctx, a, "service.access", &ss.project, &ss.env, ss.String(), summary)
	// Its uptime checks gain or lose their session (uptimeEntry.accessKey).
	c.kickUptime()
	if req.Mode == apitypes.AccessPublic {
		return apitypes.Access{Mode: apitypes.AccessPublic}, nil
	}
	return toAccess(row), nil
}

// accessUsername checks a basic-auth username: printable, no colon (it
// ends the username in the header) and no braces (Caddy placeholders).
func accessUsername(u string) (string, error) {
	u = strings.TrimSpace(u)
	switch {
	case u == "":
		return "", invalid("a password needs a username too; pass one (--username)")
	case len(u) > 64 || strings.ContainsAny(u, ":{}") || strings.ContainsFunc(u, unicode.IsControl):
		return "", invalid("username %q must be at most 64 characters, without ':', '{' or '}'", u)
	}
	return u, nil
}

// accessPasswordHash hashes a new password, or keeps the current one when
// none is given and the service already has one.
func accessPasswordHash(password string, cur store.ServiceAccess) (string, error) {
	if password == "" && cur.Mode == string(apitypes.AccessPassword) {
		return cur.PasswordHash, nil
	}
	if len(password) < minAccessPassword || len(password) > 72 {
		return "", invalid("the password must be %d to 72 characters long", minAccessPassword)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), accessBcryptCost)
	return string(h), err
}

// loginSupported checks that a server can check Dock logins: the control
// plane's own always can, a node from protocol 5 on (as it last said).
func (c *Core) loginSupported(serverID int64) error {
	if serverID == c.localID || c.nodes == nil {
		return nil
	}
	h := c.nodes.State(serverID).Hello
	if h.Protocol >= remote.AccessProtocol {
		return nil
	}
	if h.Version == "" {
		return invalid("the server hasn't connected yet, so Dock can't tell whether it can check logins; try again once it is online")
	}
	return invalid("the server runs a dock too old to check Dock logins; a platform operator has to update it, or protect the service with a password")
}

// GrantAccess lets the caller open a login-protected domain: it returns
// the domain's sign-in callback with a one-minute grant naming the
// caller, for Dock's /access page to send the browser to. With the
// nonce hash the domain's server sent along, the grant works only in the
// browser that server sent (access.SignBound). The caller
// needs read access to the service's environment, in its org whichever
// org they are acting in.
func (c *Core) GrantAccess(ctx context.Context, a Actor, req apitypes.AccessGrantRequest) (apitypes.AccessGrant, error) {
	// Only looked up, not validated: generated hostnames (under a dev
	// base domain like localhost) never passed normalizeHostname.
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.Hostname)), ".")
	if req.Nonce != "" && !access.ValidNonceHash(req.Nonce) {
		return apitypes.AccessGrant{}, invalid("this sign-in link is incomplete; open https://%s/ again to sign in", host)
	}
	d, err := c.store.GetDomainByHostname(ctx, host)
	if store.IsNotFound(err) {
		return apitypes.AccessGrant{}, notFound("%s is not a domain in Dock", host)
	}
	if err != nil {
		return apitypes.AccessGrant{}, err
	}
	ss, err := c.scopeOf(ctx, d.ServiceID)
	if err != nil {
		return apitypes.AccessGrant{}, err
	}
	if a.Kind == "user" && a.OrgID != ss.project.OrgID {
		org, err := c.store.GetOrg(ctx, ss.project.OrgID)
		if err != nil {
			return apitypes.AccessGrant{}, err
		}
		if a, err = c.actIn(ctx, a, org.Slug, apitypes.ScopeRead); err != nil {
			return apitypes.AccessGrant{}, errf(apitypes.CodeForbidden, "you don't have access to %s; ask an admin of its organization to invite you", host)
		}
	}
	if err := a.checkEnv(apitypes.ScopeRead, "opening "+host, ss.project, ss.env); err != nil {
		return apitypes.AccessGrant{}, err
	}
	row, err := c.store.GetServiceAccess(ctx, ss.svc.ID)
	if store.IsNotFound(err) || (err == nil && row.Mode != string(apitypes.AccessLogin)) {
		return apitypes.AccessGrant{}, invalid("%s doesn't need a Dock login; open it directly", host)
	}
	if err != nil {
		return apitypes.AccessGrant{}, err
	}
	key, err := c.box.Open(row.Key, accessKeyAAD(row.ServiceID))
	if err != nil {
		return apitypes.AccessGrant{}, fmt.Errorf("open access key of %s: %w", ss, err)
	}
	ing, err := c.envIngress(ctx, ss.envScope)
	if err != nil {
		return apitypes.AccessGrant{}, err
	}
	user := a.Email
	if user == "" {
		user = a.Name
	}
	// A server that left a nonce in the browser gets a grant bound to it;
	// one older than that sends none and only knows the plain grant.
	grant := access.Sign(key, access.Grant, host, user, now().Add(access.GrantTTL))
	if req.Nonce != "" {
		grant = access.SignBound(key, host, req.Nonce, user, now().Add(access.GrantTTL))
	}
	q := url.Values{"grant": {grant}, "to": {access.LocalPath(req.To)}}
	return apitypes.AccessGrant{
		URL:      c.toDomain(ss, d, nil, ing).URL + access.CallbackPath + "?" + q.Encode(),
		Hostname: host, Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
	}, nil
}

// routeAccess is a routable domain's protection for Caddy, nil when its
// service is public.
func (c *Core) routeAccess(d store.ListRoutableDomainsRow) (*node.Access, error) {
	switch apitypes.AccessMode(d.AccessMode) {
	case apitypes.AccessPassword:
		return &node.Access{Username: d.AccessUsername, PasswordHash: d.AccessPasswordHash, ProbeKey: c.probeKey(d.ServiceID)}, nil
	case apitypes.AccessLogin:
		key, err := c.box.Open(d.AccessKey, accessKeyAAD(d.ServiceID))
		if err != nil {
			return nil, fmt.Errorf("open access key of %s: %w", d.Hostname, err)
		}
		return &node.Access{Key: key, LoginURL: c.dockURL() + "/access"}, nil
	}
	return nil, nil
}

// probeKey signs the sessions of uptime probes of a password-protected
// service (ADR 0030, "Uptime probes"): derived from the secrets key and
// the service, so nothing is stored, and of no use to anyone but a probe,
// as the route passes only a session naming access.ProbeUser with it.
func (c *Core) probeKey(serviceID int64) []byte {
	return c.box.Tag("access probe key", []byte(strconv.FormatInt(serviceID, 10)))
}

// dockURL is where people reach Dock's UI: its public address, or the
// private one without it.
func (c *Core) dockURL() string {
	if u := c.publicURL(); u != "" {
		return u
	}
	return "http://" + c.cfg.Listen
}
