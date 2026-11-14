package core

import (
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/mail/mailtest"
)

// emailHarness is a harness with a public address and Dock's email set
// to a test SMTP server; events are the outbox's live events.
func emailHarness(t *testing.T) (*harness, *mailtest.Server, *subscription) {
	t.Helper()
	h := newHarness(t)
	host := "dock.example.com"
	h.c.address.Store(&host)
	srv := mailtest.Start(t)
	h.setEmail(srv)
	sub := h.c.bus.subscribe(func(e busEvent) bool { return e.ev.Type == apitypes.EventEmail && e.orgID == 0 })
	t.Cleanup(sub.close)
	return h, srv, sub
}

func (h *harness) setEmail(srv *mailtest.Server) {
	h.t.Helper()
	if _, err := h.c.SetEmail(h.ctx, testSystem, apitypes.SetEmailRequest{Host: srv.Host, Port: srv.Port, From: "dock@example.com"}); err != nil {
		h.t.Fatal(err)
	}
}

// waitEmail waits for the live event of a mail reaching status.
func waitEmail(t *testing.T, sub *subscription, status apitypes.EmailState) apitypes.EmailDelivery {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-sub.ch:
			if ev.Email.Status == status {
				return *ev.Email
			}
		case <-timeout:
			t.Fatalf("no mail reached %s", status)
		}
	}
}

// An invite's link is emailed to the person invited, and its row says
// it was sent.
func TestInviteIsEmailed(t *testing.T) {
	h, srv, sub := emailHarness(t)
	created, err := h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "kim@example.com", Role: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}
	if created.Mail.Status != apitypes.InviteMailQueued || created.Mail.ID == 0 {
		t.Fatalf("mail = %+v", created.Mail)
	}
	d := waitEmail(t, sub, apitypes.EmailSent)
	if d.Invite != created.Invite.ID || d.To != "kim@example.com" {
		t.Fatalf("delivery = %+v", d)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 || msgs[0].To[0] != "kim@example.com" || !strings.Contains(strings.ReplaceAll(msgs[0].Data, "=\r\n", ""), created.Code) {
		t.Fatalf("server took %+v", msgs)
	}
	invites, err := h.c.ListInvites(h.ctx, testSystem)
	if err != nil {
		t.Fatal(err)
	}
	if len(invites) != 1 || invites[0].Mail == nil || invites[0].Mail.Status != apitypes.InviteMailSent {
		t.Fatalf("invites = %+v", invites)
	}
	// The body, with the link, doesn't stay in the database.
	row, err := h.store.GetEmailSettings(h.ctx)
	if err != nil || row.LastOkAt == nil {
		t.Fatalf("status = %+v, %v", row, err)
	}
	var bodies int
	if err := h.store.DB.QueryRowContext(h.ctx, "SELECT count(*) FROM email_outbox WHERE body IS NOT NULL").Scan(&bodies); err != nil || bodies != 0 {
		t.Fatalf("bodies kept: %d, %v", bodies, err)
	}
}

// Without email, or without a public address for the link, nothing is
// queued and the invite says why.
func TestInviteNotEmailed(t *testing.T) {
	h := newHarness(t)
	created, err := h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "kim@example.com", Role: apitypes.ScopeRead})
	if err != nil || created.Mail.Status != apitypes.InviteMailNotSetUp {
		t.Fatalf("mail = %+v, %v", created.Mail, err)
	}
	h.setEmail(mailtest.Start(t))
	created, err = h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "bo@example.com", Role: apitypes.ScopeRead})
	if err != nil || created.Mail.Status != apitypes.InviteMailNoAddress {
		t.Fatalf("mail = %+v, %v", created.Mail, err)
	}
}

// A recipient the server refuses fails at once; a server that is down
// leaves the mail queued for a retry, which a fix of the settings brings
// forward.
func TestEmailFailuresAndRetries(t *testing.T) {
	h, srv, sub := emailHarness(t)
	srv.Reject("gone@example.com", "550 5.1.1 no such user")
	if _, err := h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "gone@example.com", Role: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	if d := waitEmail(t, sub, apitypes.EmailFailed); !strings.Contains(d.Error, "550") || d.Attempts != 1 {
		t.Fatalf("delivery = %+v", d)
	}

	srv.Down(true)
	if _, err := h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "kim@example.com", Role: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	waitEmail(t, sub, apitypes.EmailQueued) // queued
	d := waitEmail(t, sub, apitypes.EmailQueued)
	if d.Attempts != 1 || d.NextAt == nil || !d.NextAt.After(time.Now()) {
		t.Fatalf("retry = %+v", d)
	}
	st, err := h.c.EmailSettings(h.ctx, testSystem)
	if err != nil || st.Status.LastError == "" || st.Status.Queued != 1 {
		t.Fatalf("settings = %+v, %v", st, err)
	}
	srv.Down(false)
	h.setEmail(srv)
	if d := waitEmail(t, sub, apitypes.EmailSent); d.To != "kim@example.com" {
		t.Fatalf("sent = %+v", d)
	}
}

// Mail still queued when Dock stops goes out after it starts again.
func TestQueuedEmailSurvivesRestart(t *testing.T) {
	h, srv, sub := emailHarness(t)
	srv.Down(true)
	if _, err := h.c.CreateInvite(h.ctx, testSystem, apitypes.CreateInviteRequest{Email: "kim@example.com", Role: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	waitEmail(t, sub, apitypes.EmailQueued)
	waitEmail(t, sub, apitypes.EmailQueued) // the failed first try
	srv.Down(false)
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE email_outbox SET next_at = 0"); err != nil {
		t.Fatal(err)
	}
	h.restart()
	sub = h.c.bus.subscribe(func(e busEvent) bool { return e.ev.Type == apitypes.EventEmail && e.orgID == 0 })
	defer sub.close()
	select {
	case <-srv.Received():
	case <-time.After(10 * time.Second):
		t.Fatal("the queued mail wasn't sent after the restart")
	}
}

// Only operators read or set email; setting it needs a recent sign-in;
// the password is never returned.
func TestEmailSettingsAccess(t *testing.T) {
	h := newHarness(t)
	member := testSystem
	member = member.withPlatform("")
	if _, err := h.c.EmailSettings(h.ctx, member); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("member read: %v", err)
	}
	stale := testSystem
	stale.SessionID, stale.VerifiedAt = 1, time.Now().Add(-time.Hour)
	req := apitypes.SetEmailRequest{Host: "smtp.example.com", Username: "dock", Password: ptr("hunter22"), From: "Ops <ops@example.com>"}
	if _, err := h.c.SetEmail(h.ctx, stale, req); CodeOf(err) != apitypes.CodeReauthRequired {
		t.Fatalf("stale set: %v", err)
	}
	if _, err := h.c.SetEmail(h.ctx, testSystem, apitypes.SetEmailRequest{Host: "169.254.169.254", From: "ops@example.com"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("metadata host: %v", err)
	}
	st, err := h.c.SetEmail(h.ctx, testSystem, req)
	if err != nil {
		t.Fatal(err)
	}
	if !st.PasswordSet || st.Port != 587 || st.Security != apitypes.EmailStartTLS || st.From != "ops@example.com" || st.FromName != "Ops" {
		t.Fatalf("settings = %+v", st)
	}
	// Leaving the password out keeps it.
	req.Password = nil
	if st, err = h.c.SetEmail(h.ctx, testSystem, req); err != nil || !st.PasswordSet {
		t.Fatalf("kept password: %+v, %v", st, err)
	}
	if st, err = h.c.RemoveEmail(h.ctx, testSystem); err != nil || st.Configured {
		t.Fatalf("removed: %+v, %v", st, err)
	}
}
