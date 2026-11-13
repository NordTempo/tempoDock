package mail

import (
	"bytes"
	"embed"
	htmltemplate "html/template"
	"strings"
	"sync"
	texttemplate "text/template"
	"time"
)

// The mails Dock sends, one pair of files each in templates/: <name>.txt
// defines "<name>.subject" and "<name>.text", <name>.html "<name>.html"
// (shared parts in layout.html). Each has a typed data struct here and a
// function that renders it. They are parsed on first use, never at
// start-up, so `dock --help` doesn't pay for them.

//go:embed templates
var files embed.FS

// parsed holds the parsed templates.
var parsed = sync.OnceValues(func() (*templates, error) {
	text, err := texttemplate.New("").ParseFS(files, "templates/*.txt")
	if err != nil {
		return nil, err
	}
	// list makes the [label, value] pairs of the "row" template.
	list := func(s ...string) []string { return s }
	html, err := htmltemplate.New("").Funcs(htmltemplate.FuncMap{"list": list}).ParseFS(files, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &templates{text: text, html: html}, nil
})

type templates struct {
	text *texttemplate.Template
	html *htmltemplate.Template
}

// Invite is an invite to an org (ADR 0060): who asked whom in, as what,
// and the link.
type Invite struct {
	Org       string // the org's name
	Role      string // read, deploy or admin
	InvitedBy string
	Email     string
	URL       string
	Expires   time.Time
	Site      string // Dock's public host, for the footer
}

// RoleMeans says what the role lets one do, for the invite's first line.
func (i Invite) RoleMeans() string {
	switch i.Role {
	case "admin":
		return "you can manage it, its people and its services"
	case "deploy":
		return "you can deploy and run its services"
	default:
		return "you can see its projects and services"
	}
}

// ExpiresText is when the link stops working, as the mail says it.
func (i Invite) ExpiresText() string {
	return i.Expires.UTC().Format("2 January 2006 at 15:04 UTC")
}

// InviteMail renders an invite.
func InviteMail(d Invite) (Content, error) { return render("invite", d) }

// Test is the test mail an operator sends from the console or `dock
// email test`.
type Test struct {
	SentBy string
	Server string // host:port
	Site   string
}

// TestMail renders a test mail.
func TestMail(d Test) (Content, error) { return render("test", d) }

// PasswordReset is a link that sets a new password, asked for on the
// sign-in page.
type PasswordReset struct {
	Email   string
	URL     string
	Expires time.Time
	Site    string
}

// ExpiresText is when the link stops working.
func (p PasswordReset) ExpiresText() string { return p.Expires.UTC().Format("15:04 UTC") }

// PasswordResetMail renders a password reset link.
func PasswordResetMail(d PasswordReset) (Content, error) { return render("reset", d) }

// Notice tells someone their account's sign-in changed: what, when and
// by whom, and what to do if it wasn't them.
type Notice struct {
	What    string // "Your password was changed", one line
	By      string // who did it, when not the person themselves
	When    time.Time
	Email   string
	Account string // the Account page's URL; "" without a public address
	Site    string
}

// WhenText is when it happened.
func (s Notice) WhenText() string { return s.When.UTC().Format("2 January 2006 at 15:04 UTC") }

// NoticeMail renders a security notice.
func NoticeMail(d Notice) (Content, error) { return render("security", d) }

// Request is an access request waiting for an admin (ADR 0044): what a
// token asked to do, where, and the Inbox link that decides it.
type Request struct {
	ID          int64
	Title       string
	Org         string
	Project     string
	Environment string
	RequestedBy string
	Reason      string
	Expires     time.Time
	URL         string // the org's Inbox
	Account     string // the Account page, where the mail is turned off
	Site        string
}

// ExpiresText is when the request lapses undecided.
func (r Request) ExpiresText() string { return r.Expires.UTC().Format("2 January 2006 at 15:04 UTC") }

// RequestMail renders an access request.
func RequestMail(d Request) (Content, error) { return render("request", d) }

// render renders name's subject, text and HTML (if it has one).
func render(name string, data any) (Content, error) {
	t, err := parsed()
	if err != nil {
		return Content{}, err
	}
	var out Content
	var b bytes.Buffer
	if err := t.text.ExecuteTemplate(&b, name+".subject", data); err != nil {
		return out, err
	}
	out.Subject = strings.TrimSpace(b.String())
	b.Reset()
	if err := t.text.ExecuteTemplate(&b, name+".text", data); err != nil {
		return out, err
	}
	out.Text = strings.TrimSpace(b.String()) + "\n"
	if t.html.Lookup(name+".html") != nil {
		b.Reset()
		if err := t.html.ExecuteTemplate(&b, name+".html", data); err != nil {
			return out, err
		}
		out.HTML = b.String()
	}
	return out, nil
}
