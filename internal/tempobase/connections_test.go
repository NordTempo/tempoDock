package tempobase

import (
	"bytes"
	"crypto/tls"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

func TestParseConnURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want connURL
		err  string
	}{
		{url: "postgres://app:pw@db.internal/shop", want: connURL{Host: "db.internal", Port: 5432, Database: "shop", User: "app", Password: "pw", TLS: tbtypes.TLSPrefer}},
		{url: "postgresql://app:p%40ss@[::1]:6543/shop?sslmode=verify-full", want: connURL{Host: "::1", Port: 6543, Database: "shop", User: "app", Password: "p@ss", TLS: tbtypes.TLSVerifyFull}},
		{url: "postgres://app@db.internal", want: connURL{Host: "db.internal", Port: 5432, Database: "app", User: "app", TLS: tbtypes.TLSPrefer}},
		{url: "mysql://app:pw@db/shop", err: "postgres://"},
		{url: "postgres://:pw@db/shop", err: "needs a user"},
		{url: "postgres://app:pw@/shop", err: "needs a host"},
		{url: "postgres://app:pw@a,b/shop", err: "one host"},
		{url: "postgres://app:pw@db:0/shop", err: "port"},
		{url: "postgres://app:pw@db/shop/x", err: "database name alone"},
		{url: "postgres://app:pw@db/shop?connect_timeout=5", err: `"connect_timeout" is not supported`},
		{url: "postgres://app:pw@db/shop?sslmode=allow", err: "sslmode"},
	} {
		got, err := parseConnURL(tc.url)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err = %v, want %q", tc.url, err, tc.err)
			}
			if err != nil && strings.Contains(err.Error(), "pw") {
				t.Errorf("%s: error leaks the password: %v", tc.url, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s = %+v, %v; want %+v", tc.url, got, err, tc.want)
		}
	}
}

// TLS follows libpq's sslmode: prefer may fall back, require encrypts
// without verifying, verify-full checks the name.
func TestTLSConfig(t *testing.T) {
	cfg, optional, err := tlsConfig(tbtypes.TLSDisable, "db", "")
	if cfg != nil || optional || err != nil {
		t.Fatalf("disable = %v, %v, %v", cfg, optional, err)
	}
	cfg, optional, _ = tlsConfig(tbtypes.TLSPrefer, "db", "")
	if cfg == nil || !optional || !cfg.InsecureSkipVerify {
		t.Fatalf("prefer = %+v, optional %v", cfg, optional)
	}
	cfg, optional, _ = tlsConfig(tbtypes.TLSRequire, "db", "")
	if cfg == nil || optional || !cfg.InsecureSkipVerify {
		t.Fatalf("require = %+v, optional %v", cfg, optional)
	}
	cfg, _, _ = tlsConfig(tbtypes.TLSVerifyCA, "db", "")
	if cfg.VerifyConnection == nil || cfg.VerifyConnection(tls.ConnectionState{}) == nil {
		t.Fatal("verify-ca accepts a server without a certificate")
	}
	cfg, _, _ = tlsConfig(tbtypes.TLSVerifyFull, "db", "")
	if cfg.InsecureSkipVerify || cfg.ServerName != "db" {
		t.Fatalf("verify-full = %+v", cfg)
	}
	if _, _, err := tlsConfig(tbtypes.TLSVerifyFull, "db", "not pem"); err == nil {
		t.Fatal("accepted a CA that isn't PEM")
	}
	if _, _, err := tlsConfig(tbtypes.TLSPrefer, "db", "-----BEGIN CERTIFICATE-----"); err == nil {
		t.Fatal("accepted a CA with sslmode prefer")
	}
}

// The URL is sealed at rest; nothing the API returns or audits carries
// the password, and Connect gets it back.
func TestConnectionSealed(t *testing.T) {
	h := newHarness(t)
	admin := h.admin()
	c := h.connection("orders", true)
	if strings.Contains(c.URL, "s3cret") || c.URL != "postgres://owner@db.example.com:5433/orders?sslmode=require" {
		t.Fatalf("url %q", c.URL)
	}
	row, err := h.store.GetConnectionByName(h.ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.UrlSecret, []byte("s3cret")) {
		t.Fatal("url stored in the clear")
	}
	conn, err := h.Host().Connect(h.ctx, &row)
	if err != nil {
		t.Fatal(err)
	}
	if conn.Owner.User != "owner" || conn.Owner.Password != "s3cret-pw" || conn.Database != "orders" || conn.TLS == nil || conn.TLSOptional {
		t.Fatalf("Connect = %+v", conn)
	}
	events, err := h.ListAudit(h.ctx, admin, AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.Contains(e.Summary, "s3cret") {
			t.Fatalf("audit leaks the password: %q", e.Summary)
		}
	}

	// A changed URL applies at once; a rename keeps the policy and the rest.
	newURL := "postgres://other:pw2@db2.example.com/orders2?sslmode=disable"
	c, err = h.UpdateConnection(h.ctx, admin, "orders", tbtypes.UpdateConnectionRequest{Name: new("orders-v2"), URL: &newURL, Production: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "orders-v2" || c.Host != "db2.example.com" || c.TLS != tbtypes.TLSDisable || c.Production {
		t.Fatalf("updated %+v", c)
	}
	_, err = h.GetConnection(h.ctx, admin, "orders")
	wantCode(t, err, apitypes.CodeNotFound)

	_, err = h.CreateConnection(h.ctx, admin, tbtypes.CreateConnectionRequest{Name: "orders-v2", URL: newURL})
	wantCode(t, err, apitypes.CodeConflict)
	_, err = h.CreateConnection(h.ctx, admin, tbtypes.CreateConnectionRequest{Name: "Bad_Name", URL: newURL})
	wantCode(t, err, apitypes.CodeValidation)
	if err := h.DeleteConnection(h.ctx, admin, "orders-v2"); err != nil {
		t.Fatal(err)
	}
}
