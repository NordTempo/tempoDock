package apitypes

import "time"

// Dock's own email (ADR 0079): the platform's SMTP server, which
// operators set once, and the mail Dock sends through it.

// EmailSecurity is how Dock's connection to the SMTP server is protected.
type EmailSecurity string

// Email security modes.
const (
	// EmailStartTLS upgrades a plain connection with STARTTLS when the
	// server offers it, and requires it before a password is sent.
	EmailStartTLS EmailSecurity = "starttls"
	// EmailTLS is TLS from the start (port 465).
	EmailTLS EmailSecurity = "tls"
)

// EmailSettings is the platform's SMTP server as operators see it: never
// its password.
type EmailSettings struct {
	// Configured is false until an operator sets a server; then Dock
	// sends nothing and the rest is empty but LinksURL.
	Configured  bool          `json:"configured"`
	Host        string        `json:"host,omitempty"`
	Port        int           `json:"port,omitempty"`
	Security    EmailSecurity `json:"security,omitempty" enum:"starttls,tls"`
	Username    string        `json:"username,omitempty"`
	PasswordSet bool          `json:"password_set,omitempty"`
	From        string        `json:"from,omitempty" doc:"The From address"`
	FromName    string        `json:"from_name,omitempty"`
	ReplyTo     string        `json:"reply_to,omitempty"`
	UpdatedBy   string        `json:"updated_by,omitempty"`
	UpdatedAt   *time.Time    `json:"updated_at,omitempty"`
	// LinksURL is Dock's public address, where links in mail open;
	// empty without one, when mail with a link isn't sent.
	LinksURL string      `json:"links_url,omitempty"`
	Status   EmailStatus `json:"status"`
}

// EmailStatus is how sending goes: the outcome of the latest attempt
// to reach the server, and the outbox.
type EmailStatus struct {
	LastOKAt    *time.Time `json:"last_ok_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	// Queued mails wait to be sent, the next of them at NextAt.
	Queued int        `json:"queued"`
	NextAt *time.Time `json:"next_at,omitempty"`
	// FailedDay mails were given up on in the last 24 hours.
	FailedDay int `json:"failed_day"`
}

// SetEmailRequest sets the platform's SMTP server.
type SetEmailRequest struct {
	Host     string        `json:"host" minLength:"1" maxLength:"253"`
	Port     int           `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Default 465 with tls, else 587"`
	Security EmailSecurity `json:"security,omitempty" enum:"starttls,tls" doc:"Default tls on port 465, else starttls"`
	Username string        `json:"username,omitempty" maxLength:"256"`
	// Password is stored sealed and never returned. Omitted, the stored
	// one stays; empty, it is removed.
	Password *string `json:"password,omitempty" maxLength:"1024"`
	From     string  `json:"from" minLength:"3" maxLength:"254" doc:"The From address"`
	FromName string  `json:"from_name,omitempty" maxLength:"64" doc:"Default Tempo Dock"`
	ReplyTo  string  `json:"reply_to,omitempty" maxLength:"254"`
}

// TestEmailRequest sends a test mail, to the operator's own address
// unless To is set.
type TestEmailRequest struct {
	To string `json:"to,omitempty" maxLength:"254"`
}

// EmailTest is how a test mail went.
type EmailTest struct {
	To       string        `json:"to"`
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Settings EmailSettings `json:"settings"`
}

// EmailKind is what a mail is.
type EmailKind string

// Mail kinds.
const (
	EmailInvite   EmailKind = "invite"
	EmailTestKind EmailKind = "test"
)

// EmailState is where a mail is.
type EmailState string

// Mail states.
const (
	EmailQueued EmailState = "queued"
	EmailSent   EmailState = "sent"
	EmailFailed EmailState = "failed"
)

// EmailDelivery is one mail in the outbox, without its body.
type EmailDelivery struct {
	ID       int64      `json:"id"`
	Kind     EmailKind  `json:"kind" enum:"invite,test"`
	To       string     `json:"to"`
	Subject  string     `json:"subject"`
	Org      string     `json:"org,omitempty" doc:"The org it is about; empty for the platform's"`
	Invite   int64      `json:"invite,omitempty" doc:"The invite it carries"`
	Status   EmailState `json:"status" enum:"queued,sent,failed"`
	Attempts int        `json:"attempts"`
	Error    string     `json:"error,omitempty"`
	// NextAt is the next try of a queued mail.
	NextAt    *time.Time `json:"next_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
}

// EmailLog is the latest mails, newest first.
type EmailLog struct {
	Deliveries []EmailDelivery `json:"deliveries"`
}

// InviteMailStatus is what happened to an invite's mail.
type InviteMailStatus string

// Invite mail states: the outbox's, or why none was queued.
const (
	InviteMailQueued InviteMailStatus = "queued"
	InviteMailSent   InviteMailStatus = "sent"
	InviteMailFailed InviteMailStatus = "failed"
	// InviteMailNotSetUp: the platform has no SMTP server.
	InviteMailNotSetUp InviteMailStatus = "not_set_up"
	// InviteMailNoAddress: Dock has no public address for the link.
	InviteMailNoAddress InviteMailStatus = "no_address"
	// InviteMailRefused: the outbox is full, or the address got too
	// many mails in the last hour (Error says which).
	InviteMailRefused InviteMailStatus = "refused"
)

// InviteMail is an invite's mail.
type InviteMail struct {
	Status InviteMailStatus `json:"status" enum:"queued,sent,failed,not_set_up,no_address,refused"`
	// ID is the mail's, for its live `email` events; 0 when none was
	// queued.
	ID    int64  `json:"id,omitempty"`
	Error string `json:"error,omitempty"`
	// At is when it was sent or given up on.
	At *time.Time `json:"at,omitempty"`
}
