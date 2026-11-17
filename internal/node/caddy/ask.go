package caddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/node"
)

// hostSet is the set of hostnames Caddy has routes for.
type hostSet map[string]struct{}

// askSet is what AskHandler answers from: the hostnames with routes, and
// the names of the loaded certificates (ADR 0090), under which nothing is
// issued on demand.
type askSet struct {
	hosts   hostSet
	covered []string
}

// allows reports whether host may get an on-demand certificate: it has a
// route, and no loaded certificate covers it.
func (s *askSet) allows(host string) bool {
	if _, ok := s.hosts[host]; !ok {
		return false
	}
	return !slices.ContainsFunc(s.covered, func(n string) bool { return covers(n, host) })
}

// certNames are the names certs cover.
func certNames(certs []node.Certificate) []string {
	var names []string
	for _, c := range certs {
		names = append(names, c.Names...)
	}
	return names
}

// routeHosts collects the hostnames of routes, redirects included: they
// need a certificate to redirect over HTTPS. TunnelOnly routes don't.
func routeHosts(routes []node.Route) *hostSet {
	s := hostSet{}
	for _, r := range routes {
		if r.TunnelOnly {
			continue // resolves to Cloudflare: no certificate of ours
		}
		for _, h := range r.Hostnames {
			s[h] = struct{}{}
		}
	}
	return &s
}

// AskHandler answers Caddy's on-demand TLS check (GET ?domain=<hostname>)
// from the routes this Proxy last applied: 200 if the hostname has a route
// and no loaded certificate covers it, 404 if not. A node mounts it on its loopback-only internal listener and
// points Options.AskURL at it, so certificates keep issuing while the
// control plane is unreachable (ADR 0014, section 5).
//
// A hostname without a route (a service that isn't running yet) gets no
// certificate until its route is applied.
//
// After a restart and before the first ApplyRoutes, it reads the hostnames
// from Caddy's running config, which Caddy restored with --resume.
func (p *Proxy) AskHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts, err := p.knownHosts(r.Context())
		if err != nil {
			// Caddy refuses the certificate on any non-2xx and asks again
			// on a later handshake.
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if !hosts.allows(strings.ToLower(r.URL.Query().Get("domain"))) {
			http.NotFound(w, r)
		}
	})
}

// knownHosts returns the hostnames and covered names of the last applied
// config, reading them from Caddy once if nothing was applied since
// start-up.
func (p *Proxy) knownHosts(ctx context.Context) (*askSet, error) {
	if h := p.hosts.Load(); h != nil {
		return h, nil
	}
	p.seedMu.Lock()
	defer p.seedMu.Unlock()
	if h := p.hosts.Load(); h != nil {
		return h, nil
	}
	h, err := p.runningHosts(ctx)
	if err != nil {
		return nil, err
	}
	// An ApplyRoutes that finished meanwhile is newer; keep it.
	p.hosts.CompareAndSwap(nil, h)
	return p.hosts.Load(), nil
}

// runningHosts reads the hostnames matched by the routes of Caddy's
// running config, and the names its loaded certificates' connection
// policies match.
func (p *Proxy) runningHosts(ctx context.Context) (*askSet, error) {
	resp, err := p.admin(ctx, http.MethodGet, "/config/apps/http/servers", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("caddy: read running config: %s", resp.Status)
	}
	var servers map[string]struct {
		Routes   []route            `json:"routes"`
		Policies []connectionPolicy `json:"tls_connection_policies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
		return nil, fmt.Errorf("caddy: read running config: %w", err)
	}
	s := &askSet{hosts: hostSet{}}
	for name, srv := range servers {
		if name == tunnelServer {
			continue // TunnelOnly routes: no certificate of ours
		}
		for _, p := range srv.Policies {
			if p.Match != nil && p.CertificateSelection != nil {
				s.covered = append(s.covered, p.Match.SNI...)
			}
		}
		for _, r := range srv.Routes {
			for _, m := range r.Match {
				for _, h := range m.Host {
					s.hosts[h] = struct{}{}
				}
			}
		}
	}
	return s, nil
}
