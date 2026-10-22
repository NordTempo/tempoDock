package cli

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/core"
)

// lockedBuffer is a bytes.Buffer safe to read while a command writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var printedCode = regexp.MustCompile(`enter the code ([A-Z]{4}-[A-Z]{4})`)

// browserLogin runs `dock login --no-browser` against srv, lets decide act
// on the code it prints, and returns the exit code, stdout and stderr.
func browserLogin(t *testing.T, srv *testServer, decide func(code string) error) (int, string, string) {
	t.Helper()
	var out, errOut lockedBuffer
	exit := make(chan int, 1)
	go func() {
		exit <- run(context.Background(), []string{"login", srv.url, "--no-browser"}, &output.IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !printedCode.MatchString(errOut.String()) {
		if time.Now().After(deadline) {
			t.Fatalf("no code printed:\n%s", errOut.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := decide(printedCode.FindStringSubmatch(errOut.String())[1]); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exit:
		return code, out.String(), errOut.String()
	case <-time.After(5 * time.Second):
		t.Fatalf("login still waiting after the decision:\n%s", errOut.String())
		return 0, "", ""
	}
}

func TestBrowserLogin(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	ctx := context.Background()
	if _, err := srv.core.CreateUser(ctx, core.System, apitypes.CreateUserRequest{
		Email: "ada@example.com", Name: "Ada", Password: "a long enough password", Role: apitypes.ScopeDeploy,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, ada, err := srv.core.Login(ctx, "ada@example.com", "a long enough password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	code, _, stderr := browserLogin(t, srv, func(c string) error { return srv.core.DecideCLILogin(ctx, ada, c, "127.0.0.1", false) })
	if code != int(output.ExitAuth) || !strings.Contains(stderr, "denied") {
		t.Fatalf("denied login: exit %d\n%s", code, stderr)
	}

	code, stdout, stderr := browserLogin(t, srv, func(c string) error { return srv.core.DecideCLILogin(ctx, ada, c, "127.0.0.1", true) })
	if code != 0 || !strings.Contains(stdout, "Logged in as ada@example.com (deploy) on "+srv.url) || !strings.Contains(stderr, "Open "+srv.url+"/cli-login and enter the code ") {
		t.Fatalf("approved login: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if me := ok[apitypes.Me](t, "whoami"); me.Kind != "token" || me.Scope != apitypes.ScopeDeploy || !strings.HasPrefix(me.Name, "cli: ") {
		t.Fatalf("whoami = %+v", me)
	}

	// Logging out revokes the token on the server, not just locally.
	cfg, _ := loadConfig()
	token := cfg.Contexts[cfg.CurrentContext].Token
	if code, stdout, stderr := exec(t, "logout"); code != 0 || !strings.Contains(stdout, "revoked") {
		t.Fatalf("logout: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if _, err := srv.core.AuthenticateToken(ctx, token); core.CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("token still valid after logout: %v", err)
	}
}
