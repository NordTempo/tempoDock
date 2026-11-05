//go:build !dockcli

package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/store"
)

// `dock admin reset-2fa` removes a user's factors and recovery codes on
// the server, and says so; an unknown email is not found.
func TestAdminResetTwoFactor(t *testing.T) {
	setup(t)
	dir, cfg := controlPlaneDir(t)
	if code, _, stderr := exec(t, "admin", "bootstrap", "--config", cfg, "--email", "ada@example.com"); code != 0 {
		t.Fatalf("bootstrap: exit %d: %s", code, stderr)
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.DB.Exec(`INSERT INTO user_factors (user_id, kind, name, secret, created_at) SELECT id, 'totp', 'phone', x'00', datetime('now') FROM users;
		INSERT INTO recovery_codes (user_id, hash) SELECT id, 'h' FROM users`); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := exec(t, "admin", "reset-2fa", "--config", cfg, "ada@example.com")
	if code != 0 || !strings.Contains(stdout, "Reset the two-factor sign-in of ada@example.com") {
		t.Fatalf("reset-2fa: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var left int
	if err := st.DB.QueryRow("SELECT (SELECT count(*) FROM user_factors) + (SELECT count(*) FROM recovery_codes)").Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d factors and recovery codes left (%v)", left, err)
	}
	if code, _, stderr := exec(t, "admin", "reset-2fa", "--config", cfg, "nobody@example.com"); code != int(output.ExitNotFound) || !strings.Contains(stderr, "dock user list") {
		t.Errorf("unknown email: exit %d %q", code, stderr)
	}
}

// `dock admin sso-only <org> off` is the break-glass for an org whose
// identity provider is down: on the server it turns the requirement off,
// which upgraded orgs never have on; only off is done there, and an
// unknown org is not found.
func TestAdminSSOOnlyOff(t *testing.T) {
	setup(t)
	dir, cfg := controlPlaneDir(t)
	if code, _, stderr := exec(t, "admin", "bootstrap", "--config", cfg, "--email", "ada@example.com"); code != 0 {
		t.Fatalf("bootstrap: exit %d: %s", code, stderr)
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var slug string
	var on bool
	if err := st.DB.QueryRow("SELECT slug, sso_only FROM orgs").Scan(&slug, &on); err != nil || on {
		t.Fatalf("new org %q: sso_only %v (%v)", slug, on, err)
	}
	if _, err := st.DB.Exec("UPDATE orgs SET sso_only = 1"); err != nil {
		t.Fatal(err)
	}

	if code, _, _ := exec(t, "admin", "sso-only", "--config", cfg, slug, "on"); code != int(output.ExitUsage) {
		t.Errorf("sso-only on: exit %d", code)
	}
	code, stdout, stderr := exec(t, "admin", "sso-only", "--config", cfg, slug, "off")
	if code != 0 || !strings.Contains(stdout, "no longer requires single sign-on") {
		t.Fatalf("sso-only off: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if err := st.DB.QueryRow("SELECT sso_only FROM orgs").Scan(&on); err != nil || on {
		t.Fatalf("sso_only %v after the break-glass (%v)", on, err)
	}
	if code, _, _ := exec(t, "admin", "sso-only", "--config", cfg, "nope", "off"); code != int(output.ExitNotFound) {
		t.Errorf("unknown org: exit %d", code)
	}
}
