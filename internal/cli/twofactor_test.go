package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// An organization that requires two-factor sign-in refuses a member's
// `dock login` token approved without one, and says how to get one that
// counts; the platform's policy is shown and set by operators.
func TestTwoFactorPolicies(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	ctx := context.Background()
	if _, err := srv.core.CreateUser(ctx, sys, apitypes.CreateUserRequest{
		Email: "ada@example.com", Name: "Ada", Password: "a long enough password", Role: apitypes.ScopeDeploy,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := srv.core.Login(ctx, "ada@example.com", "a long enough password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := browserLogin(t, srv, func(c string) error { return srv.core.DecideCLILogin(ctx, res.Actor, c, "127.0.0.1", true) }); code != 0 {
		t.Fatalf("login: exit %d\n%s", code, stderr)
	}
	if tf := ok[apitypes.TwoFactor](t, "me", "two-factor"); len(tf.Factors) != 0 {
		t.Fatalf("me two-factor = %+v", tf)
	}

	// The operator's API token sets the policies.
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	if code, _, stderr := exec(t, "org", "two-factor", "maybe"); code != int(output.ExitUsage) || !strings.Contains(stderr, "on or off") {
		t.Fatalf("org two-factor maybe: exit %d %q", code, stderr)
	}
	if o := ok[apitypes.Org](t, "org", "two-factor", "on"); !o.RequireTwoFactor {
		t.Fatalf("org two-factor on = %+v", o)
	}
	if o := ok[apitypes.Org](t, "org", "two-factor"); !o.RequireTwoFactor {
		t.Fatalf("org two-factor = %+v", o)
	}
	if p := ok[apitypes.PlatformSecurity](t, "security", "operators-two-factor", "on"); !p.OperatorsTwoFactor {
		t.Fatalf("security operators-two-factor on = %+v", p)
	}
	if p := ok[apitypes.PlatformSecurity](t, "security"); !p.OperatorsTwoFactor {
		t.Fatalf("security = %+v", p)
	}

	// Ada's token used no second factor: refused, with the way out.
	t.Setenv(clientconfig.EnvURL, "")
	t.Setenv(clientconfig.EnvToken, "")
	code, _, stderr := exec(t, "project", "list")
	if code != int(output.ExitAuth) || !strings.Contains(stderr, "requires two-factor sign-in") || !strings.Contains(stderr, "run `dock login` again") {
		t.Fatalf("project list without a second factor: exit %d\n%s", code, stderr)
	}
	if code, stdout, stderr := exec(t, "whoami"); code != 0 || !strings.Contains(stdout, "used no second factor") || !strings.Contains(stderr, "requires two-factor sign-in") {
		t.Fatalf("whoami: exit %d\n%s\n%s", code, stdout, stderr)
	}
}
