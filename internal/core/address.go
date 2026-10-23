package core

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Dock's public address (ADR 0029): a hostname the control plane's Caddy
// serves the API and UI on, with a certificate like an app domain's,
// proxied to the loopback [public_listen]. The private [listen] address
// keeps working beside it. Operators set it from the console or with
// `dock address set`.

// publicHost is Dock's public address, "" when it has none.
func (c *Core) publicHost() string {
	if p := c.address.Load(); p != nil {
		return *p
	}
	return ""
}

// loadPublicHost reads the public address into memory, where routes and
// certificate checks read it.
func (c *Core) loadPublicHost(ctx context.Context) error {
	host, err := c.store.GetPublicHost(ctx)
	if store.IsNotFound(err) {
		host, err = "", nil
	}
	if err != nil {
		return fmt.Errorf("load public address: %w", err)
	}
	c.address.Store(&host)
	return nil
}

// controlURL is the URL of a hostname the control plane's Caddy serves:
// https://, or http:// when it serves plain HTTP, with the port when it
// isn't the default. A tunnel server is https:// at Cloudflare's edge.
func (c *Core) controlURL(host string) string {
	scheme, port := "https", c.cfg.Caddy.HTTPSPort
	switch {
	case c.cfg.Ingress.Mode == config.IngressCloudflareTunnel:
		port = 443
	case c.cfg.Caddy.DisableTLS:
		scheme, port = "http", c.cfg.Caddy.HTTPPort
	}
	if (scheme == "https" && port != 443) || (scheme == "http" && port != 80) {
		host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return scheme + "://" + host
}

// publicURL is Dock's public URL, "" without a public address. Links Dock
// hands out (invites, commit statuses) use it.
func (c *Core) publicURL() string {
	if host := c.publicHost(); host != "" {
		return c.controlURL(host)
	}
	return ""
}

// Address describes where Dock is reached and checks the public address's
// DNS and certificate now. Operators only.
func (c *Core) Address(ctx context.Context, a Actor) (apitypes.Address, error) {
	if err := a.requireOperator("reading Dock's address"); err != nil {
		return apitypes.Address{}, err
	}
	return c.describeAddress(ctx)
}

// SetAddress makes host Dock's public address: the control plane's Caddy
// starts serving the API and UI there and gets it a certificate once its
// DNS points at the server. Operators only.
func (c *Core) SetAddress(ctx context.Context, a Actor, req apitypes.SetAddressRequest) (apitypes.Address, error) {
	if err := a.requireOperator("setting Dock's address"); err != nil {
		return apitypes.Address{}, err
	}
	host, err := normalizeHostname(req.Hostname)
	if err != nil {
		return apitypes.Address{}, err
	}
	if host == c.cfg.Hooks.PublicHost {
		return apitypes.Address{}, errf(apitypes.CodeHostnameTaken, "%s is this server's webhook hostname ([hooks] public_host); use another hostname", host)
	}
	d, err := c.store.GetDomainByHostname(ctx, host)
	switch {
	case err == nil:
		owner := "a service"
		if ss, err := c.scopeOf(ctx, d.ServiceID); err == nil {
			owner = ss.String()
		}
		return apitypes.Address{}, errf(apitypes.CodeHostnameTaken, "%s is a domain of %s; remove it there first, or use another hostname", host, owner)
	case !store.IsNotFound(err):
		return apitypes.Address{}, err
	}
	old := c.publicHost()
	if host != old {
		if err := c.storePublicHost(ctx, a, host); err != nil {
			return apitypes.Address{}, err
		}
		summary := "set Dock's public address to " + c.controlURL(host)
		if old != "" {
			summary = fmt.Sprintf("moved Dock's public address from %s to %s", old, host)
		}
		c.audit(ctx, a.platform(), "platform.address_set", nil, nil, host, summary)
	}
	return c.describeAddress(ctx)
}

// RemoveAddress stops serving Dock on its public address; it stays
// reachable at its private one. Operators only.
func (c *Core) RemoveAddress(ctx context.Context, a Actor) (apitypes.Address, error) {
	if err := a.requireOperator("removing Dock's address"); err != nil {
		return apitypes.Address{}, err
	}
	if old := c.publicHost(); old != "" {
		if err := c.storePublicHost(ctx, a, ""); err != nil {
			return apitypes.Address{}, err
		}
		c.audit(ctx, a.platform(), "platform.address_removed", nil, nil, old, "removed Dock's public address "+old)
	}
	return c.describeAddress(ctx)
}

// storePublicHost saves the public address and routes it. Every online
// server is rerouted too: login-protected services send visitors to
// Dock's address (ADR 0030); offline ones pick it up when they reconnect.
// A failed route apply leaves Caddy as it was; the address's status shows
// it, and the next apply (any deploy, or a restart) retries.
func (c *Core) storePublicHost(ctx context.Context, a Actor, host string) error {
	if err := c.store.SetPublicHost(ctx, store.SetPublicHostParams{PublicHost: host, UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
		return err
	}
	c.address.Store(&host)
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return err
	}
	for _, s := range servers {
		if s.ID != c.local.ID && !c.online(s.ID) {
			continue
		}
		if err := c.applyRoutes(ctx, s.ID); err != nil {
			c.log.Error("apply routes for the public address", "host", host, "server", s.Name, "err", err)
		}
	}
	return nil
}

// describeAddress is Address without the permission check. The check is
// one DNS lookup and one TLS handshake with the local Caddy; the first
// handshake is what makes Caddy issue the certificate.
func (c *Core) describeAddress(ctx context.Context) (apitypes.Address, error) {
	out := apitypes.Address{PrivateURL: "http://" + c.cfg.Listen, DNSRecords: []apitypes.DNSRecord{}}
	host := c.publicHost()
	if host == "" {
		return out, nil
	}
	srv, err := c.store.GetLocalServer(ctx)
	if err != nil {
		return out, err
	}
	ing := c.ingressOf(srv)
	out.Hostname, out.URL = host, c.controlURL(host)
	if ing.mode == apitypes.IngressCloudflareTunnel {
		if ing.tunnel != "" {
			out.DNSRecords = append(out.DNSRecords, apitypes.DNSRecord{Type: "CNAME", Name: host, Value: ing.tunnel})
		}
	} else {
		for _, ip := range ing.publicIPs {
			out.DNSRecords = append(out.DNSRecords, apitypes.DNSRecord{Type: "A", Name: host, Value: ip})
		}
	}
	check, err := c.rt.CheckDomain(ctx, host)
	switch {
	case err != nil:
		out.Status, out.StatusReason = apitypes.DomainError, "could not check: "+err.Error()
		return out, nil
	case !check.DNSOK:
		out.Status, out.StatusReason = apitypes.DomainWaitingForDNS, check.Error
		if out.StatusReason == "" {
			out.StatusReason = fmt.Sprintf("%s resolves to %v; expected %v", host, check.ResolvedIPs, check.ExpectedIPs)
		}
	case ing.plainHTTP || check.CertificateOK:
		out.Status = apitypes.DomainActive
	case check.Error != "":
		out.Status, out.StatusReason = apitypes.DomainError, check.Error
	default:
		out.Status = apitypes.DomainIssuingCertificate
		out.StatusReason = "DNS is right and Caddy is getting a certificate. If this lasts more than a few minutes, check that ports 80 and 443 reach this server."
	}
	at := now()
	out.CheckedAt, out.ResolvedIPs = &at, slices.Clone(check.ResolvedIPs)
	return out, nil
}

// withControlRoutes adds the control plane's own routes to its server's
// routing table: Dock's public address, proxied to [public_listen] with
// the client's address, and the webhook route on [hooks] public_host,
// only paths under gitHookPath, to the loopback hooks listener. A domain
// with the same hostname (added before the setting) keeps it, and that
// route is left out.
func (c *Core) withControlRoutes(serverID int64, routes []node.Route) []node.Route {
	if serverID != c.local.ID {
		return routes
	}
	taken := func(host string) bool {
		for _, r := range routes {
			if slices.Contains(r.Hostnames, host) {
				c.log.Warn("a control plane hostname is also a domain; it is not routed", "host", host)
				return true
			}
		}
		return false
	}
	var own []node.Route
	if host := c.publicHost(); host != "" && !taken(host) {
		own = append(own, node.Route{Hostnames: []string{host}, Upstream: c.cfg.PublicListen, ClientIPHeader: ClientIPHeader})
	}
	if host := c.cfg.Hooks.PublicHost; host != "" && !taken(host) {
		own = append(own, node.Route{Hostnames: []string{host}, Upstream: c.cfg.Hooks.Listen, PathPrefix: gitHookPath})
	}
	return append(routes, own...)
}

// ClientIPHeader is the request header Caddy sets to the client's address
// on Dock's public address, overwriting whatever the client sent. The
// public listener reads it, and nothing else does (api.Public).
const ClientIPHeader = "X-Dock-Client-Ip"
