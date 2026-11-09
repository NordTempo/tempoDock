// Package cloudflaretest is an in-memory fake of the part of Cloudflare's
// API that package cloudflare calls, for tests: one account with zones,
// their DNS records, redirect rules and tunnels, and switches to make
// calls fail.
package cloudflaretest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/cloudflare"
)

// Token is the API token the fake accepts until Revoke.
const Token = "test-cloudflare-token"

// AccountID is the fake account's ID.
const AccountID = "acc0000000000000000000000000000a"

// Server is the fake. Lock Mu to read or change its state from a test.
type Server struct {
	*httptest.Server

	Mu sync.Mutex
	// Token is the accepted token; "" refuses every token.
	Token   string
	Account cloudflare.Account
	Zones   []cloudflare.Zone
	// Records are the DNS records by zone ID.
	Records map[string][]cloudflare.Record
	// Tunnels are by ID.
	Tunnels map[string]*Tunnel
	// Rulesets are the zones' redirect entry point rulesets, by zone ID.
	Rulesets map[string]*cloudflare.Ruleset
	// NoRedirects answers 403 to ruleset calls, as for a token without
	// Single Redirect Edit.
	NoRedirects bool
	// DeniedZones answer 403 to DNS calls, as for a token without DNS Edit
	// there.
	DeniedZones map[string]bool
	// NoTunnels answers 403 to tunnel calls.
	NoTunnels bool
	// Fail, when set, may answer a request with status (and Retry-After
	// seconds) instead of handling it; 0 handles it.
	Fail func(r *http.Request) (status, retryAfter int)
	// Calls are the requests handled, as "METHOD /path".
	Calls []string

	next int
}

// Tunnel is a fake tunnel with what the API exposes of it.
type Tunnel struct {
	cloudflare.Tunnel
	Token      string
	Config     *cloudflare.TunnelConfig
	Connectors []cloudflare.Connector
}

// New starts a fake with one account and the given zones (by name). It is
// closed when the test ends.
func New(t testing.TB, zones ...string) *Server {
	s := &Server{
		Token:       Token,
		Account:     cloudflare.Account{ID: AccountID, Name: "Test account"},
		Records:     map[string][]cloudflare.Record{},
		Tunnels:     map[string]*Tunnel{},
		Rulesets:    map[string]*cloudflare.Ruleset{},
		DeniedZones: map[string]bool{},
	}
	for _, z := range zones {
		s.Zones = append(s.Zones, cloudflare.Zone{ID: s.id("zone"), Name: z, Status: "active"})
	}
	s.Server = httptest.NewServer(s.mux())
	t.Cleanup(s.Close)
	return s
}

// ZoneID returns the ID of the zone named name.
func (s *Server) ZoneID(name string) string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for _, z := range s.Zones {
		if z.Name == name {
			return z.ID
		}
	}
	return ""
}

// Named returns the records named name, in any zone.
func (s *Server) Named(name string) []cloudflare.Record {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	var out []cloudflare.Record
	for _, rs := range s.Records {
		for _, r := range rs {
			if r.Name == name {
				out = append(out, r)
			}
		}
	}
	return out
}

// AddRecord adds a record to the zone named zone, as if made by hand.
func (s *Server) AddRecord(zone string, r cloudflare.Record) cloudflare.Record {
	id := s.ZoneID(zone)
	s.Mu.Lock()
	defer s.Mu.Unlock()
	r.ID = s.id("rec")
	s.Records[id] = append(s.Records[id], r)
	return r
}

// Rules returns the redirect rules of the zone named zone.
func (s *Server) Rules(zone string) []cloudflare.Rule {
	id := s.ZoneID(zone)
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if rs := s.Rulesets[id]; rs != nil {
		return slices.Clone(rs.Rules)
	}
	return nil
}

// AddRule adds a redirect rule to the zone named zone, as if made in the
// dashboard.
func (s *Server) AddRule(zone string, r cloudflare.Rule) cloudflare.Rule {
	id := s.ZoneID(zone)
	s.Mu.Lock()
	defer s.Mu.Unlock()
	rs := s.Rulesets[id]
	if rs == nil {
		rs = &cloudflare.Ruleset{ID: s.id("set"), Kind: "zone", Phase: cloudflare.RedirectPhase}
		s.Rulesets[id] = rs
	}
	r.ID = s.id("rule")
	rs.Rules = append(rs.Rules, r)
	return r
}

// AddTunnel adds a tunnel, as if made in the dashboard. configSrc is
// "cloudflare" or "local".
func (s *Server) AddTunnel(name, configSrc string) *Tunnel {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.addTunnel(name, configSrc)
}

// Revoke makes every token invalid, like a token revoked in the dashboard.
func (s *Server) Revoke() {
	s.Mu.Lock()
	s.Token = ""
	s.Mu.Unlock()
}

// CallCount counts the handled requests that start with prefix
// ("POST /zones", "GET").
func (s *Server) CallCount(prefix string) int {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	n := 0
	for _, c := range s.Calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (s *Server) addTunnel(name, configSrc string) *Tunnel {
	id := fmt.Sprintf("%08d-0000-4000-8000-%012d", s.next+1, s.next+1)
	s.next++
	t := &Tunnel{
		Tunnel: cloudflare.Tunnel{ID: id, Name: name, AccountTag: s.Account.ID, ConfigSrc: configSrc, Status: "inactive"},
		Token:  "tunnel-token-" + id,
	}
	s.Tunnels[id] = t
	return t
}

func (s *Server) id(kind string) string {
	s.next++
	return fmt.Sprintf("%s%028d", kind, s.next)
}

// handler is one fake endpoint; it runs with s.Mu held.
type handler func(w http.ResponseWriter, r *http.Request)

func (s *Server) mux() http.Handler {
	m := http.NewServeMux()
	route := func(pattern string, h handler) {
		m.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			s.Mu.Lock()
			defer s.Mu.Unlock()
			s.Calls = append(s.Calls, r.Method+" "+r.URL.Path)
			if s.Fail != nil {
				if status, after := s.Fail(r); status != 0 {
					if after > 0 {
						w.Header().Set("Retry-After", strconv.Itoa(after))
					}
					fail(w, status, 10000+status, "injected failure")
					return
				}
			}
			if s.Token == "" || r.Header.Get("Authorization") != "Bearer "+s.Token {
				fail(w, http.StatusUnauthorized, 9109, "Invalid access token")
				return
			}
			h(w, r)
		})
	}
	route("GET /user/tokens/verify", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, map[string]string{"status": "active"})
	})
	route("GET /accounts", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, []cloudflare.Account{s.Account})
	})
	route("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		if a := r.URL.Query().Get("account.id"); a != "" && a != s.Account.ID {
			ok(w, []cloudflare.Zone{})
			return
		}
		ok(w, s.Zones)
	})
	dns := func(h func(w http.ResponseWriter, r *http.Request, zone string)) handler {
		return func(w http.ResponseWriter, r *http.Request) {
			zone := r.PathValue("zone")
			if !slices.ContainsFunc(s.Zones, func(z cloudflare.Zone) bool { return z.ID == zone }) {
				fail(w, http.StatusNotFound, 7003, "Could not route to /zones/"+zone)
				return
			}
			if s.DeniedZones[zone] {
				fail(w, http.StatusForbidden, 10000, "Authentication error")
				return
			}
			h(w, r, zone)
		}
	}
	route("GET /zones/{zone}/dns_records", dns(func(w http.ResponseWriter, r *http.Request, zone string) {
		name := r.URL.Query().Get("name.exact")
		out := []cloudflare.Record{}
		for _, rec := range s.Records[zone] {
			if name == "" || strings.EqualFold(rec.Name, name) {
				out = append(out, rec)
			}
		}
		ok(w, out)
	}))
	route("POST /zones/{zone}/dns_records", dns(func(w http.ResponseWriter, r *http.Request, zone string) {
		var rec cloudflare.Record
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			fail(w, http.StatusBadRequest, 9207, "bad request")
			return
		}
		if s.conflicts(zone, rec) {
			fail(w, http.StatusBadRequest, 81053, "An A, AAAA, or CNAME record with that host already exists.")
			return
		}
		rec.ID = s.id("rec")
		s.Records[zone] = append(s.Records[zone], rec)
		ok(w, rec)
	}))
	route("PATCH /zones/{zone}/dns_records/{id}", dns(func(w http.ResponseWriter, r *http.Request, zone string) {
		i := slices.IndexFunc(s.Records[zone], func(rec cloudflare.Record) bool { return rec.ID == r.PathValue("id") })
		if i < 0 {
			fail(w, http.StatusNotFound, 81044, "Record does not exist.")
			return
		}
		var rec cloudflare.Record
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			fail(w, http.StatusBadRequest, 9207, "bad request")
			return
		}
		rec.ID = s.Records[zone][i].ID
		s.Records[zone][i] = rec
		ok(w, rec)
	}))
	route("DELETE /zones/{zone}/dns_records/{id}", dns(func(w http.ResponseWriter, r *http.Request, zone string) {
		i := slices.IndexFunc(s.Records[zone], func(rec cloudflare.Record) bool { return rec.ID == r.PathValue("id") })
		if i < 0 {
			fail(w, http.StatusNotFound, 81044, "Record does not exist.")
			return
		}
		s.Records[zone] = slices.Delete(s.Records[zone], i, i+1)
		ok(w, map[string]string{"id": r.PathValue("id")})
	}))

	rulesets := func(h func(w http.ResponseWriter, r *http.Request, zone string)) handler {
		return dns(func(w http.ResponseWriter, r *http.Request, zone string) {
			if s.NoRedirects {
				fail(w, http.StatusForbidden, 10000, "Authentication error")
				return
			}
			h(w, r, zone)
		})
	}
	// ruleset finds the zone's redirect ruleset named in the path.
	ruleset := func(h func(w http.ResponseWriter, r *http.Request, rs *cloudflare.Ruleset)) handler {
		return rulesets(func(w http.ResponseWriter, r *http.Request, zone string) {
			rs := s.Rulesets[zone]
			if rs == nil || rs.ID != r.PathValue("set") {
				fail(w, http.StatusNotFound, 10003, "could not find ruleset")
				return
			}
			h(w, r, rs)
		})
	}
	decodeRule := func(w http.ResponseWriter, r *http.Request) (cloudflare.Rule, bool) {
		var rule cloudflare.Rule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil || rule.Expression == "" || rule.Action == "" {
			fail(w, http.StatusBadRequest, 20021, "invalid rule")
			return rule, false
		}
		return rule, true
	}
	route("GET /zones/{zone}/rulesets/phases/{phase}/entrypoint", rulesets(func(w http.ResponseWriter, r *http.Request, zone string) {
		rs := s.Rulesets[zone]
		if rs == nil || r.PathValue("phase") != cloudflare.RedirectPhase {
			fail(w, http.StatusNotFound, 10003, "could not find entrypoint ruleset in the "+r.PathValue("phase")+" phase")
			return
		}
		ok(w, rs)
	}))
	route("POST /zones/{zone}/rulesets", rulesets(func(w http.ResponseWriter, r *http.Request, zone string) {
		var rs cloudflare.Ruleset
		if err := json.NewDecoder(r.Body).Decode(&rs); err != nil || rs.Phase != cloudflare.RedirectPhase || rs.Kind != "zone" {
			fail(w, http.StatusBadRequest, 20021, "invalid ruleset")
			return
		}
		if s.Rulesets[zone] != nil {
			fail(w, http.StatusBadRequest, 20217, "A similar configuration with rules already exists and overwriting will have unintended consequences")
			return
		}
		rs.ID = s.id("set")
		for i := range rs.Rules {
			rs.Rules[i].ID = s.id("rule")
		}
		s.Rulesets[zone] = &rs
		ok(w, rs)
	}))
	route("POST /zones/{zone}/rulesets/{set}/rules", ruleset(func(w http.ResponseWriter, r *http.Request, rs *cloudflare.Ruleset) {
		rule, valid := decodeRule(w, r)
		if !valid {
			return
		}
		rule.ID = s.id("rule")
		rs.Rules = append(rs.Rules, rule)
		ok(w, rs)
	}))
	route("PATCH /zones/{zone}/rulesets/{set}/rules/{rule}", ruleset(func(w http.ResponseWriter, r *http.Request, rs *cloudflare.Ruleset) {
		i := slices.IndexFunc(rs.Rules, func(rule cloudflare.Rule) bool { return rule.ID == r.PathValue("rule") })
		if i < 0 {
			fail(w, http.StatusNotFound, 10003, "could not find rule")
			return
		}
		rule, valid := decodeRule(w, r)
		if !valid {
			return
		}
		rule.ID = rs.Rules[i].ID
		rs.Rules[i] = rule
		ok(w, rs)
	}))
	route("DELETE /zones/{zone}/rulesets/{set}/rules/{rule}", ruleset(func(w http.ResponseWriter, r *http.Request, rs *cloudflare.Ruleset) {
		i := slices.IndexFunc(rs.Rules, func(rule cloudflare.Rule) bool { return rule.ID == r.PathValue("rule") })
		if i < 0 {
			fail(w, http.StatusNotFound, 10003, "could not find rule")
			return
		}
		rs.Rules = slices.Delete(rs.Rules, i, i+1)
		ok(w, rs)
	}))

	tunnels := func(h func(w http.ResponseWriter, r *http.Request)) handler {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("account") != s.Account.ID {
				fail(w, http.StatusForbidden, 10000, "Authentication error")
				return
			}
			if s.NoTunnels {
				fail(w, http.StatusForbidden, 10000, "Authentication error")
				return
			}
			h(w, r)
		}
	}
	one := func(h func(w http.ResponseWriter, r *http.Request, t *Tunnel)) handler {
		return tunnels(func(w http.ResponseWriter, r *http.Request) {
			t := s.Tunnels[r.PathValue("id")]
			if t == nil || t.DeletedAt != nil {
				fail(w, http.StatusNotFound, 1003, "Tunnel not found")
				return
			}
			h(w, r, t)
		})
	}
	route("GET /accounts/{account}/cfd_tunnel", tunnels(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		out := []cloudflare.Tunnel{}
		for _, t := range s.Tunnels {
			if (name == "" || t.Name == name) && t.DeletedAt == nil {
				out = append(out, t.Tunnel)
			}
		}
		ok(w, out)
	}))
	route("POST /accounts/{account}/cfd_tunnel", tunnels(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name      string `json:"name"`
			ConfigSrc string `json:"config_src"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			fail(w, http.StatusBadRequest, 1001, "bad request")
			return
		}
		ok(w, s.addTunnel(body.Name, body.ConfigSrc).Tunnel)
	}))
	route("GET /accounts/{account}/cfd_tunnel/{id}", one(func(w http.ResponseWriter, _ *http.Request, t *Tunnel) {
		ok(w, t.Tunnel)
	}))
	route("DELETE /accounts/{account}/cfd_tunnel/{id}", one(func(w http.ResponseWriter, _ *http.Request, t *Tunnel) {
		if len(t.Connectors) > 0 {
			fail(w, http.StatusBadRequest, 1022, "Cannot delete a tunnel with active connections")
			return
		}
		now := time.Now()
		t.DeletedAt = &now
		ok(w, t.Tunnel)
	}))
	route("GET /accounts/{account}/cfd_tunnel/{id}/token", one(func(w http.ResponseWriter, _ *http.Request, t *Tunnel) {
		ok(w, t.Token)
	}))
	route("GET /accounts/{account}/cfd_tunnel/{id}/configurations", one(func(w http.ResponseWriter, _ *http.Request, t *Tunnel) {
		ok(w, map[string]any{"tunnel_id": t.ID, "config": t.Config})
	}))
	route("PUT /accounts/{account}/cfd_tunnel/{id}/configurations", one(func(w http.ResponseWriter, r *http.Request, t *Tunnel) {
		if t.ConfigSrc != "cloudflare" {
			fail(w, http.StatusBadRequest, 1056, "Tunnel is not remotely managed")
			return
		}
		var body struct {
			Config cloudflare.TunnelConfig `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Config.Ingress) == 0 {
			fail(w, http.StatusBadRequest, 1055, "At least one ingress rule needs to be defined")
			return
		}
		t.Config = &body.Config
		ok(w, map[string]any{"tunnel_id": t.ID, "config": t.Config})
	}))
	route("GET /accounts/{account}/cfd_tunnel/{id}/connections", one(func(w http.ResponseWriter, _ *http.Request, t *Tunnel) {
		ok(w, append([]cloudflare.Connector{}, t.Connectors...))
	}))
	route("DELETE /accounts/{account}/cfd_tunnel/{id}/connections", one(func(w http.ResponseWriter, _ *http.Request, t *Tunnel) {
		t.Connectors = nil
		ok(w, nil)
	}))
	return m
}

// conflicts reports whether rec can't be created next to the zone's
// records: a CNAME shares its name with no A, AAAA or CNAME, and the
// other way round.
func (s *Server) conflicts(zone string, rec cloudflare.Record) bool {
	for _, r := range s.Records[zone] {
		if !strings.EqualFold(r.Name, rec.Name) {
			continue
		}
		if r.Type == "CNAME" || rec.Type == "CNAME" {
			return true
		}
		if r.Type == rec.Type && r.Content == rec.Content {
			return true
		}
	}
	return false
}

func ok(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result, "result_info": map[string]int{"page": 1, "total_pages": 1}})
}

func fail(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": code, "message": msg}}, "result": nil})
}
