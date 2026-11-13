package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/mail/mailtest"
)

// An operator sets the SMTP server, sends a test, and later changes one
// setting without repeating the rest.
func TestEmailCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	smtp := mailtest.Start(t)

	if st := ok[apitypes.EmailSettings](t, "email"); st.Configured {
		t.Fatalf("configured from the start: %+v", st)
	}
	st := ok[apitypes.EmailSettings](t, "email", "set", "--host", smtp.Host, "--port", itoa(int64(smtp.Port)), "--from", "dock@example.com")
	if !st.Configured || st.Security != apitypes.EmailStartTLS {
		t.Fatalf("set = %+v", st)
	}
	if res := ok[apitypes.EmailTest](t, "email", "test", "--to", "ops@example.com"); !res.OK {
		t.Fatalf("test = %+v", res)
	}
	if got := smtp.Messages(); len(got) != 1 || got[0].To[0] != "ops@example.com" {
		t.Fatalf("server took %+v", got)
	}
	if st := ok[apitypes.EmailSettings](t, "email", "set", "--reply-to", "help@example.com"); st.Host != smtp.Host || st.ReplyTo != "help@example.com" {
		t.Fatalf("kept the rest? %+v", st)
	}
	code, stdout, stderr := exec(t, "email", "log")
	if code != 0 || !strings.Contains(stdout, "ops@example.com") || !strings.Contains(stdout, "sent") {
		t.Fatalf("log = %d\n%s\n%s", code, stdout, stderr)
	}
}
