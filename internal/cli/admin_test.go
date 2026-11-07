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

// `dock admin password-sign-in on` is the break-glass for a platform
// whose identity providers all fail while passwords are off: on the
// server it turns them on, and --password-for gives an account a new one.
func TestAdminPasswordSignInOn(t *testing.T) {
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
	if _, err := st.DB.Exec(`INSERT INTO platform_settings (id, password_sign_in, updated_by, updated_at) VALUES (1, 0, 'test', datetime())
		ON CONFLICT (id) DO UPDATE SET password_sign_in = 0`); err != nil {
		t.Fatal(err)
	}

	if code, _, _ := exec(t, "admin", "password-sign-in", "--config", cfg, "off"); code != int(output.ExitUsage) {
		t.Errorf("password-sign-in off: exit %d", code)
	}
	if code, _, _ := exec(t, "admin", "password-sign-in", "--config", cfg, "on", "--password-for", "nobody@example.com"); code != int(output.ExitNotFound) {
		t.Errorf("unknown email: exit %d", code)
	}
	code, stdout, stderr := exec(t, "admin", "password-sign-in", "--config", cfg, "on", "--password-for", "ada@example.com")
	if code != 0 || !strings.Contains(stdout, "Password sign-in is on") || !strings.Contains(stdout, "New password of ada@example.com") {
		t.Fatalf("password-sign-in on: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var on bool
	if err := st.DB.QueryRow("SELECT password_sign_in FROM platform_settings").Scan(&on); err != nil || !on {
		t.Fatalf("password_sign_in %v after the break-glass (%v)", on, err)
	}
}
