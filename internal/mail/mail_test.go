package mail_test

import (
	"context"
	"io/fs"
	"mime"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/mail"
	"github.com/NordTempo/tempoDock/internal/mail/mailtest"
)

var dial = (&net.Dialer{}).DialContext

// A batch goes over one connection; a refused recipient fails only its
// own message, and the rest are sent.
func TestSendBatchWithARefusedRecipient(t *testing.T) {
	srv := mailtest.Start(t)
	srv.Reject("gone@example.com", "550 5.1.1 no such user")
	from, _ := mail.ParseAddress("Tempo Dock <dock@example.com>")
	msg := func(to string) mail.Message {
		return mail.Message{From: *from, To: []mail.Address{{Address: to}}, Content: mail.Content{Subject: "Hi", Text: "hello\n"}}
	}
	errs := map[int]error{}
	err := mail.Send(context.Background(), dial, mail.Server{Host: srv.Host, Port: srv.Port}, []mail.Message{msg("a@example.com"), msg("gone@example.com"), msg("b@example.com")}, func(i int, err error) { errs[i] = err })
	if err != nil {
		t.Fatal(err)
	}
	if errs[0] != nil || errs[2] != nil || errs[1] == nil || !mail.Permanent(errs[1]) {
		t.Fatalf("outcomes = %v", errs)
	}
	got := srv.Messages()
	if len(got) != 2 || got[0].To[0] != "a@example.com" || got[1].To[0] != "b@example.com" {
		t.Fatalf("server took %+v", got)
	}
}

// A server that answers 421 fails the connection, worth a retry.
func TestSendServerDown(t *testing.T) {
	srv := mailtest.Start(t)
	srv.Down(true)
	from, _ := mail.ParseAddress("dock@example.com")
	err := mail.Send(context.Background(), dial, mail.Server{Host: srv.Host, Port: srv.Port}, []mail.Message{{From: *from, To: []mail.Address{{Address: "a@example.com"}}}}, func(int, error) { t.Fatal("no message should be tried") })
	if err == nil || mail.Permanent(err) {
		t.Fatalf("err = %v, want a temporary failure", err)
	}
}

// A password is never sent over a connection that didn't get TLS.
func TestNoPasswordWithoutTLS(t *testing.T) {
	srv := mailtest.Start(t)
	from, _ := mail.ParseAddress("dock@example.com")
	err := mail.Send(context.Background(), dial, mail.Server{Host: srv.Host, Port: srv.Port, Username: "u", Password: "p"}, []mail.Message{{From: *from, To: []mail.Address{{Address: "a@example.com"}}}}, func(int, error) {})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v", err)
	}
}

// What users write (a name, an org's name in the subject) can't add a
// header.
func TestBuildKeepsHeadersClosed(t *testing.T) {
	from, _ := mail.ParseAddress("dock@example.com")
	m := mail.Message{
		From:    mail.Address{Name: "Ops\r\nBcc: x@evil.example", Address: from.Address},
		To:      []mail.Address{{Address: "a@example.com"}},
		Content: mail.Content{Subject: "Join acme\r\nBcc: x@evil.example", Text: "hi", HTML: "<p>hi</p>"},
	}
	b, err := mail.Build(m, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(b), "\r\n\r\n")
	for line := range strings.SplitSeq(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("a header was injected:\n%s", head)
		}
	}
	if !strings.Contains(head, "multipart/alternative") {
		t.Fatalf("no multipart:\n%s", head)
	}
	if _, err := mail.ParseAddress("a@example.com\r\nBcc: x@evil.example"); err == nil {
		t.Fatal("an address with a line break parsed")
	}
}

// The invite says who, what and until when, escapes what users wrote in
// its HTML, and has a subject that decodes as written.
func TestInviteMail(t *testing.T) {
	c, err := mail.InviteMail(mail.Invite{
		Org: "Acme <script>", Role: "deploy", InvitedBy: "Ana Berg", Email: "kim@example.com",
		URL: "https://dock.example.com/invite/dki_x", Expires: time.Date(2026, 11, 20, 14, 2, 0, 0, time.UTC), Site: "dock.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "Join Acme <script> on Tempo Dock" {
		t.Fatalf("subject = %q", c.Subject)
	}
	for _, want := range []string{"Ana Berg invited you", "deploy role", "https://dock.example.com/invite/dki_x", "20 November 2026 at 14:02 UTC", "kim@example.com"} {
		if !strings.Contains(c.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, c.Text)
		}
	}
	if strings.Contains(c.HTML, "<script>") || !strings.Contains(c.HTML, "Acme &lt;script&gt;") {
		t.Fatalf("HTML doesn't escape the org's name:\n%s", c.HTML)
	}
	if dec, err := new(mime.WordDecoder).DecodeHeader(mime.QEncoding.Encode("utf-8", c.Subject)); err != nil || dec != c.Subject {
		t.Fatalf("subject round trip = %q, %v", dec, err)
	}
}

// Dock's copy has no em or en dashes, mail included.
func TestTemplatesHaveNoDashes(t *testing.T) {
	err := filepath.WalkDir("templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.ContainsAny(string(b), "\u2014\u2013") {
			t.Errorf("%s has an em or en dash", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
