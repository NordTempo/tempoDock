package core

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mail/mailtest"
	"github.com/NordTempo/tempoDock/internal/store"
)

// mailTo waits for the next mail the server takes and returns its body,
// with quoted-printable soft breaks joined.
func mailTo(t *testing.T, srv *mailtest.Server, to string) string {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		for _, m := range srv.Messages() {
			if len(m.To) > 0 && m.To[0] == to {
				return strings.ReplaceAll(m.Data, "=\r\n", "")
			}
		}
		select {
		case <-srv.Received():
		case <-timeout:
			t.Fatalf("no mail to %s", to)
		}
	}
}

// A reset link from the sign-in page sets a new password once, ends the
// account's sessions and sends a notice; an unknown address gets nothing
// and the same answer.
func TestPasswordResetByEmail(t *testing.T) {
	h, srv, _ := emailHarness(t)
	if _, err := h.c.CreateUser(h.ctx, testSystem, apitypes.CreateUserRequest{Email: "ada@example.com", Name: "Ada", Password: "the old password", Role: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	if m, err := h.c.SignInMethods(h.ctx); err != nil || !m.PasswordReset {
		t.Fatalf("methods = %+v, %v", m, err)
	}
	if err := h.c.RequestPasswordReset(h.ctx, "nobody@example.com", "198.51.100.1"); err != nil {
		t.Fatalf("unknown address: %v", err)
	}
	if err := h.c.RequestPasswordReset(h.ctx, "ada@example.com", "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`/reset-password/(dpr_[A-Za-z0-9_-]+)`).FindStringSubmatch(mailTo(t, srv, "ada@example.com"))
	if code == nil {
		t.Fatal("the mail has no reset link")
	}
	if got := srv.Messages(); len(got) != 1 {
		t.Fatalf("mails: %d, want only Ada's", len(got))
	}
	if info, err := h.c.PasswordReset(h.ctx, code[1], "198.51.100.1"); err != nil || info.Email != "ada@example.com" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if err := h.c.ResetPasswordWithLink(h.ctx, code[1], apitypes.PasswordResetRequest{Password: "short"}, "198.51.100.1"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("short password: %v", err)
	}
	// The link proves the email of an account that hadn't (ssoUser).
	h.exec(`UPDATE users SET email_proven = 0 WHERE email = 'ada@example.com'`)
	if err := h.c.ResetPasswordWithLink(h.ctx, code[1], apitypes.PasswordResetRequest{Password: "a brand new password"}, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	if u, err := h.store.GetUserByEmail(h.ctx, "ada@example.com"); err != nil || !u.EmailProven {
		t.Fatalf("email not proven by the reset: %+v, %v", u.EmailProven, err)
	}
	if err := h.c.ResetPasswordWithLink(h.ctx, code[1], apitypes.PasswordResetRequest{Password: "another new password"}, "198.51.100.1"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("second use: %v", err)
	}
	if _, err := h.c.Login(h.ctx, "ada@example.com", "a brand new password", "198.51.100.2"); err != nil {
		t.Fatalf("sign in with the new password: %v", err)
	}
	waitFor(t, func() bool { return len(srv.Messages()) == 2 })
	if notice := srv.Messages()[1].Data; !strings.Contains(notice, "password was changed") {
		t.Fatalf("notice:\n%s", notice)
	}
}

// Email alert channels hold only recipients and send through Dock's
// server; a channel with an SMTP server of its own from before moves on
// start, its server becoming the platform's when it has none.
func TestEmailChannelsUseDocksServer(t *testing.T) {
	h, srv, _ := emailHarness(t)
	h.channel(apitypes.CreateAlertChannelRequest{Name: "mail", Kind: apitypes.ChannelEmail, To: []string{"ops@example.com"}})
	res, err := h.c.TestAlertChannel(h.ctx, testSystem, false, "mail")
	if err != nil || !res.OK {
		t.Fatalf("test = %+v, %v", res, err)
	}
	if body := mailTo(t, srv, "ops@example.com"); !strings.Contains(body, "Test alert from dock") {
		t.Fatalf("mail:\n%s", body)
	}

	// A platform channel from before ADR 0079, and no platform server.
	if _, err := h.c.RemoveEmail(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(channelConfig{SMTP: &legacySMTP{Host: srv.Host, Port: srv.Port, From: "Dock <dock@example.com>", To: []string{"oncall@example.com"}}})
	sealed, err := h.c.box.Seal(old, channelAAD(nil, "pager"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateAlertChannel(h.ctx, store.CreateAlertChannelParams{Name: "pager", Kind: "email", Target: "oncall@example.com", Config: sealed, MinSeverity: "warning", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	st, err := h.c.EmailSettings(h.ctx, testSystem)
	if err != nil || !st.Configured || st.Host != srv.Host || st.From != "dock@example.com" || st.FromName != "Dock" {
		t.Fatalf("settings = %+v, %v", st, err)
	}
	ch, err := h.store.GetAlertChannel(h.ctx, store.GetAlertChannelParams{Org: 0, Name: "pager"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := h.c.openChannel(ch)
	if err != nil || cfg.SMTP != nil || len(cfg.To) != 1 || cfg.To[0] != "oncall@example.com" {
		t.Fatalf("moved config = %+v, %v", cfg, err)
	}
}

// A new access request is mailed to the org's admins, unless they turned
// it off.
func TestAccessRequestMail(t *testing.T) {
	rh := newRequestHarness(t)
	host := "dock.example.com"
	rh.c.address.Store(&host)
	srv := mailtest.Start(t)
	rh.setEmail(srv)
	u, err := rh.c.CreateUser(rh.ctx, testSystem, apitypes.CreateUserRequest{Email: "boss@example.com", Name: "Boss", Password: "a long enough password", Role: apitypes.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	le := rh.ask("1", "launch prices")
	body := mailTo(t, srv, "boss@example.com")
	for _, want := range []string{"claude asks", "shop/production", "launch prices", "https://dock.example.com/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("mail lacks %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "/inbox") || le.Request == 0 {
		t.Fatalf("no Inbox link:\n%s", body)
	}

	boss := Actor{Kind: "user", UserID: u.ID, Email: u.Email, Name: u.Email, OrgID: 1}
	if _, err := rh.c.SetMyEmail(rh.ctx, boss, apitypes.MyEmail{AccessRequests: false}); err != nil {
		t.Fatal(err)
	}
	before := countEmail(t, rh.harness, "access_request")
	_ = rh.ask("2", "")
	if after := countEmail(t, rh.harness, "access_request"); after != before {
		t.Fatalf("mailed with access request email off: %d -> %d", before, after)
	}
}

// countEmail counts the outbox's mails of a kind.
func countEmail(t *testing.T, h *harness, kind string) int {
	t.Helper()
	var n int
	if err := h.store.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM email_outbox WHERE kind = ?", kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// resetWithLink asks for a reset link for email, reads it from the mail
// that brings it (other mail to the address aside) and sets password
// with it.
func resetWithLink(t *testing.T, h *harness, srv *mailtest.Server, email, password string) {
	t.Helper()
	before := len(srv.Messages())
	if err := h.c.RequestPasswordReset(h.ctx, email, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
	link := regexp.MustCompile(`/reset-password/(dpr_[A-Za-z0-9_-]+)`)
	var code []string
	waitFor(t, func() bool {
		for _, m := range srv.Messages()[before:] {
			if code = link.FindStringSubmatch(strings.ReplaceAll(m.Data, "=\r\n", "")); code != nil {
				return true
			}
		}
		return false
	})
	if err := h.c.ResetPasswordWithLink(h.ctx, code[1], apitypes.PasswordResetRequest{Password: password}, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
}

// An account made from an invite never proved its email: whoever held
// the invite's code set it up. A reset link reaches the mailbox, so the
// first one is the email's owner taking the account, as a single sign-on
// does (takeOver): the second factors, recovery codes and tokens set up
// before go with the sessions. A proven account's reset keeps its
// factors, as it always did.
func TestResetLinkTakesAnUnprovenAccount(t *testing.T) {
	h, srv, _ := emailHarness(t)
	freshLimits(h)
	admin := h.in(h.member("ann@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	inv, err := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: "vic@example.com", Role: apitypes.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.c.SignUpWithInvite(h.ctx, inv.Code, apitypes.InviteSignUpRequest{Name: "Vic", Password: "the inviter's password"}, testClient)
	if err != nil {
		t.Fatal(err)
	}
	early := session(t, h, res.Secret)
	addTOTP(t, h, early)
	early = session(t, h, early.secret)
	tok, err := h.c.CreateToken(h.ctx, h.in(early.a, "test"), apitypes.CreateTokenRequest{Name: "kept", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}

	asToken, err := h.c.AuthenticateToken(h.ctx, tok.Secret, "")
	if err != nil {
		t.Fatal(err)
	}
	following := h.followAs(asToken)

	resetWithLink(t, h, srv, "vic@example.com", "the owner's own password")
	endsWithAccess(t, "what a token made before the reset follows", following)
	if n, err := h.store.CountUserFactors(h.ctx, early.a.UserID); err != nil || n != 0 {
		t.Fatalf("second factors left on the account: %d, %v", n, err)
	}
	if n, err := h.store.CountRecoveryCodes(h.ctx, early.a.UserID); err != nil || n != 0 {
		t.Fatalf("recovery codes left on the account: %d, %v", n, err)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, tok.Secret, ""); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("a token made before the reset: %v", err)
	}
	if _, err := h.c.AuthenticateSession(h.ctx, early.secret, "", false); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("a session from before the reset: %v", err)
	}
	if in, err := h.c.Login(h.ctx, "vic@example.com", "the owner's own password", testClient); err != nil || in.Pending {
		t.Fatalf("the owner signing in: pending %v, %v", in.Pending, err)
	}

	// Proven now: the owner's own second factor survives their next reset.
	owner, err := h.c.Login(h.ctx, "vic@example.com", "the owner's own password", testClient)
	if err != nil {
		t.Fatal(err)
	}
	addTOTP(t, h, session(t, h, owner.Secret))
	resetWithLink(t, h, srv, "vic@example.com", "the owner's next password")
	if in, err := h.c.Login(h.ctx, "vic@example.com", "the owner's next password", testClient); err != nil || !in.Pending {
		t.Fatalf("a proven account's second factor after a reset: pending %v, %v", in.Pending, err)
	}
}

// Testing an email channel mails its recipients at once, past the outbox
// and its limits: an organization does it once a minute, each test is in
// the audit log, and a channel takes no more recipients than the API's
// schema says.
func TestEmailChannelTestsAreLimited(t *testing.T) {
	h, srv, _ := emailHarness(t)
	h.channel(apitypes.CreateAlertChannelRequest{Name: "mail", Kind: apitypes.ChannelEmail, To: []string{"ops@example.com"}})
	if res, err := h.c.TestAlertChannel(h.ctx, testSystem, false, "mail"); err != nil || !res.OK {
		t.Fatalf("the first test: %+v, %v", res, err)
	}
	for range 5 {
		if _, err := h.c.TestAlertChannel(h.ctx, testSystem, false, "mail"); CodeOf(err) != apitypes.CodeRateLimited {
			t.Fatalf("a test right after: %v", err)
		}
	}
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("%d mails left for 6 tests", n)
	}
	// A minute on, the next test goes out.
	h.c.alerts.mu.Lock()
	h.c.alerts.mailTested[testSystem.OrgID] = now().Add(-emailTestEvery - time.Second)
	h.c.alerts.mu.Unlock()
	if res, err := h.c.TestAlertChannel(h.ctx, testSystem, false, "mail"); err != nil || !res.OK {
		t.Fatalf("a test a minute later: %+v, %v", res, err)
	}
	if n := h.audited("alert.channel_test"); n != 2 {
		t.Fatalf("audited tests: %d", n)
	}

	var many []string
	for i := range maxChannelRecipients + 1 {
		many = append(many, fmt.Sprintf("p%d@example.com", i))
	}
	if _, err := h.c.CreateAlertChannel(h.ctx, testSystem, false, apitypes.CreateAlertChannelRequest{Name: "crowd", Kind: apitypes.ChannelEmail, To: many}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a channel with %d recipients: %v", len(many), err)
	}
}
