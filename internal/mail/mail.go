// Package mail sends Dock's email over SMTP: a batch of messages over one
// connection, each a plain-text part with an optional HTML one, built
// from the embedded templates (templates.go). It knows nothing of where
// it may connect: callers pass the dialer, so core's egress guard decides.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Security is how a connection to the SMTP server is protected.
type Security string

const (
	// StartTLS connects in plain text and upgrades with STARTTLS when the
	// server offers it; it must, before a password is sent. Port 587.
	StartTLS Security = "starttls"
	// TLS is TLS from the start ("implicit TLS", SMTPS). Port 465.
	TLS Security = "tls"
)

// Server is an SMTP server and how to sign in to it.
type Server struct {
	Host     string
	Port     int // 0: 465 for TLS, else 587
	Security Security
	Username string // "": no login
	Password string
}

// Addr is the server's host:port, with the default port filled in.
func (s Server) Addr() string {
	port := s.Port
	if port == 0 {
		port = 587
		if s.Security == TLS {
			port = 465
		}
	}
	return net.JoinHostPort(s.Host, strconv.Itoa(port))
}

// Address is an email address with an optional name, as net/mail has it.
type Address = mail.Address

// Message is one email. Addresses come from ParseAddress, so they hold
// no line breaks; Subject is made one line and encoded here.
type Message struct {
	From    Address
	ReplyTo *Address
	To      []Address
	Content
}

// Content is what a message says: its subject, its text and, optionally,
// the same as HTML.
type Content struct {
	Subject string
	Text    string
	HTML    string
}

// Dialer opens the TCP connection to the server.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Send sends msgs in order over one connection and calls done with each
// message's outcome. A message the server refuses (a recipient it
// doesn't know) doesn't stop the rest. Send returns an error when the
// connection, TLS, the login or the session failed; done wasn't called
// for the messages after that, which weren't tried. ctx's deadline
// bounds the whole conversation.
func Send(ctx context.Context, dial Dialer, s Server, msgs []Message, done func(i int, err error)) error {
	addr := s.Addr()
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if s.Security == TLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("TLS with %s: %w", addr, err)
		}
		conn = tc
	}
	cl, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SMTP %s: %w", addr, err)
	}
	defer func() { _ = cl.Close() }()
	if s.Security != TLS {
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("SMTP STARTTLS: %w", err)
			}
		} else if s.Username != "" {
			return fmt.Errorf("%s doesn't offer STARTTLS; not sending the password unencrypted (use TLS on port 465)", addr)
		}
	}
	if s.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("SMTP login: %w", err)
		}
	}
	for i, m := range msgs {
		err := sendOne(cl, m)
		done(i, err)
		if err != nil {
			// Start the next message from a clean session; a server that
			// can't is in no state to take more.
			if rerr := cl.Reset(); rerr != nil {
				return fmt.Errorf("SMTP after a refused message: %w", rerr)
			}
		}
	}
	return cl.Quit()
}

// sendOne sends one message over an open session.
func sendOne(cl *smtp.Client, m Message) error {
	data, err := Build(m, time.Now())
	if err != nil {
		return err
	}
	if err := cl.Mail(m.From.Address); err != nil {
		return fmt.Errorf("SMTP sender %s: %w", m.From.Address, err)
	}
	for _, to := range m.To {
		if err := cl.Rcpt(to.Address); err != nil {
			return fmt.Errorf("SMTP recipient %s: %w", to.Address, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP data: %w", err)
	}
	return nil
}

// Permanent reports whether err is the server refusing for good (a 5xx
// answer, like 550 no such user): sending the same message again won't
// help. Connection errors and 4xx answers are worth a retry.
func Permanent(err error) bool {
	tpe, ok := errors.AsType[*textproto.Error](err)
	return ok && tpe.Code >= 500
}

// Build is m as it goes over the wire: headers, then the text, or a
// multipart/alternative of the text and the HTML. Every header value is
// an address written by net/mail (which encodes names) or Dock's own,
// and the subject is one encoded line, so nothing in a message can add
// a header.
func Build(m Message, at time.Time) ([]byte, error) {
	if len(m.To) == 0 {
		return nil, errors.New("a message needs a recipient")
	}
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", m.From.String())
	to := make([]string, len(m.To))
	for i, a := range m.To {
		to[i] = a.String()
	}
	header("To", strings.Join(to, ", "))
	if m.ReplyTo != nil {
		header("Reply-To", m.ReplyTo.String())
	}
	header("Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Subject)))
	header("Date", at.UTC().Format(time.RFC1123Z))
	header("Message-ID", messageID(m.From.Address))
	header("MIME-Version", "1.0")
	// Out-of-office replies and the like aren't sent to automatic mail.
	header("Auto-Submitted", "auto-generated")
	if m.HTML == "" {
		header("Content-Type", "text/plain; charset=utf-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, m.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	mw := multipart.NewWriter(&b)
	header("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	b.WriteString("\r\n")
	for _, part := range [][2]string{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part[0] + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeQP(w, part[1]); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// writeQP writes s quoted-printable, with CRLF line ends.
func writeQP(w io.Writer, s string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(qp, strings.ReplaceAll(s, "\r\n", "\n")); err != nil {
		return err
	}
	return qp.Close()
}

// messageID is a new Message-ID at the sender's domain.
func messageID(from string) string {
	domain := "dock.invalid"
	if at := strings.LastIndexByte(from, '@'); at >= 0 && at < len(from)-1 {
		domain = from[at+1:]
	}
	var r [12]byte
	_, _ = rand.Read(r[:])
	return "<" + hex.EncodeToString(r[:]) + "@" + domain + ">"
}

// oneLine joins s's lines with spaces.
func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// ParseAddress parses one address ("Dock <dock@example.com>" or a bare
// one) and refuses line breaks, which would end a header.
func ParseAddress(s string) (*Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, fmt.Errorf("%q is not an email address", s)
	}
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("%q is not an email address", s)
	}
	return a, nil
}
