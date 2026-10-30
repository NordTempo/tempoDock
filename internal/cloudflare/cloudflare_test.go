package cloudflare_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/cloudflare/cloudflaretest"
)

func client(srv *httptest.Server, token string) *cloudflare.Client {
	return &cloudflare.Client{API: srv.URL, Token: token}
}

// TestList follows result_info across pages.
func TestList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		_, _ = fmt.Fprintf(w, `{"success":true,"result":[{"id":"z%s","name":"zone%s.example"}],"result_info":{"page":%s,"total_pages":3}}`, page, page, page)
	}))
	defer srv.Close()
	zones, err := client(srv, "t").Zones(context.Background(), "acc")
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 3 || zones[2].Name != "zone3.example" {
		t.Fatalf("zones = %+v, want 3 pages' worth", zones)
	}
}

// TestErrors maps Cloudflare's answers onto Error, and never puts the
// token into one.
func TestErrors(t *testing.T) {
	const token = "secret-token-value"
	cases := []struct {
		name                       string
		status                     int
		header                     string
		temporary, denied, missing bool
		retryAfter                 time.Duration
	}{
		{name: "revoked", status: 401, denied: true},
		{name: "forbidden", status: 403, denied: true},
		{name: "rate limited", status: 429, header: "300", temporary: true, retryAfter: 5 * time.Minute},
		{name: "down", status: 502, temporary: true},
		{name: "missing", status: 404, missing: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
			}))
			defer srv.Close()
			_, err := client(srv, token).Tunnel(context.Background(), "acc", "id")
			e, ok := cloudflare.AsError(err)
			if !ok {
				t.Fatalf("err = %v, want *cloudflare.Error", err)
			}
			if e.Temporary() != tc.temporary || e.Denied() != tc.denied || e.NotFound() != tc.missing || e.RetryAfter != tc.retryAfter {
				t.Errorf("err = %+v: temporary %v denied %v not found %v retry after %v", e, e.Temporary(), e.Denied(), e.NotFound(), e.RetryAfter)
			}
			if !e.Has(10000) || strings.Contains(err.Error(), token) {
				t.Errorf("err = %q", err)
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		_, err := client(srv, token).Zones(context.Background(), "acc")
		e, ok := cloudflare.AsError(err)
		if !ok || e.Status != 0 || !e.Temporary() || strings.Contains(err.Error(), token) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(time.Second) }))
		defer srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if _, err := client(srv, token).Zones(ctx, "acc"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context's", err)
		}
	})
}

// TestVerifyToken falls back to the account's endpoint, where
// account-owned tokens verify.
func TestVerifyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			w.WriteHeader(401)
			_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`)
		case "/accounts/acc/tokens/verify":
			_, _ = fmt.Fprint(w, `{"success":true,"result":{"status":"active"}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := client(srv, "t")
	if err := c.VerifyToken(context.Background(), "acc"); err != nil {
		t.Fatalf("account-owned token: %v", err)
	}
	if err := c.VerifyToken(context.Background(), ""); err == nil {
		t.Fatal("without an account the user endpoint's refusal stands")
	}
}

// TestTunnelLifecycle runs the tunnel calls against the fake.
func TestTunnelLifecycle(t *testing.T) {
	fake := cloudflaretest.New(t)
	c := &cloudflare.Client{API: fake.URL, Token: cloudflaretest.Token}
	ctx := context.Background()
	tun, err := c.CreateTunnel(ctx, cloudflaretest.AccountID, "dock-x-homelab")
	if err != nil {
		t.Fatal(err)
	}
	if found, err := c.TunnelNamed(ctx, cloudflaretest.AccountID, "dock-x-homelab"); err != nil || found == nil || found.ID != tun.ID {
		t.Fatalf("TunnelNamed = %+v, %v", found, err)
	}
	cfg := cloudflare.TunnelConfig{Ingress: []cloudflare.IngressRule{{Service: "http://127.0.0.1:80"}}}
	if err := c.SetTunnelConfig(ctx, cloudflaretest.AccountID, tun.ID, cfg); err != nil {
		t.Fatal(err)
	}
	if got, err := c.TunnelConfig(ctx, cloudflaretest.AccountID, tun.ID); err != nil || len(got.Ingress) != 1 {
		t.Fatalf("TunnelConfig = %+v, %v", got, err)
	}
	if tok, err := c.TunnelToken(ctx, cloudflaretest.AccountID, tun.ID); err != nil || tok == "" {
		t.Fatalf("TunnelToken = %q, %v", tok, err)
	}
	fake.Mu.Lock()
	fake.Tunnels[tun.ID].Connectors = []cloudflare.Connector{{ID: "c1"}}
	fake.Mu.Unlock()
	if err := c.DeleteTunnel(ctx, cloudflaretest.AccountID, tun.ID); err != nil {
		t.Fatalf("DeleteTunnel with a connector: %v", err)
	}
	if err := c.DeleteTunnel(ctx, cloudflaretest.AccountID, tun.ID); err != nil {
		t.Fatalf("DeleteTunnel again: %v", err)
	}
	if found, _ := c.TunnelNamed(ctx, cloudflaretest.AccountID, "dock-x-homelab"); found != nil {
		t.Fatal("deleted tunnel still found")
	}
}
