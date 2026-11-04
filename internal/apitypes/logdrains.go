package apitypes

import "time"

// LogDrainKind is where a log drain sends an environment's output (ADR 0046).
type LogDrainKind string

const (
	// DrainHTTP POSTs application/x-ndjson: one JSON object per line.
	DrainHTTP LogDrainKind = "http"
	// DrainSyslog sends RFC 5424 messages over tcp, tls or udp.
	DrainSyslog LogDrainKind = "syslog"
	// DrainLoki pushes to Loki's /loki/api/v1/push.
	DrainLoki LogDrainKind = "loki"
)

// LogDrain is a configured destination for an environment's app output.
// Its URL, headers and token are never returned.
type LogDrain struct {
	ID   int64        `json:"id"`
	Kind LogDrainKind `json:"kind" enum:"http,syslog,loki"`
	Name string       `json:"name"`
	// Target is where lines go, without credentials: scheme and host.
	Target    string         `json:"target" example:"tcp://logs.example.com:514"`
	CreatedAt time.Time      `json:"created_at"`
	CreatedBy string         `json:"created_by"`
	Status    LogDrainStatus `json:"status"`
}

// LogDrainStatus is how a drain's delivery goes since the daemon started;
// counters start at zero on every restart.
type LogDrainStatus struct {
	Sent    int64 `json:"sent" doc:"Lines delivered"`
	Dropped int64 `json:"dropped" doc:"Lines dropped because the queue was full (the sink is slow or failing)"`
	// LastError is the last failed delivery's error, until one succeeds.
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	LastSentAt  *time.Time `json:"last_sent_at,omitempty"`
}

// AddLogDrainRequest adds a log drain to an environment.
type AddLogDrainRequest struct {
	Kind LogDrainKind `json:"kind" enum:"http,syslog,loki"`
	// URL is http(s):// for http and loki (loki: the server's base URL, or
	// its push URL), tcp://, tls:// or udp:// host[:port] for syslog. A
	// user and password in an http(s) URL are sent as basic auth.
	URL  string `json:"url" minLength:"1" maxLength:"2048" doc:"Stored encrypted, never returned"`
	Name string `json:"name,omitempty" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"Default: the kind"`
	// Headers are sent with every request (http and loki), e.g. a token.
	Headers map[string]string `json:"headers,omitempty" doc:"http and loki: request headers, stored encrypted"`
	// Token is sent as Authorization: Bearer (http and loki).
	Token string `json:"token,omitempty" maxLength:"4096" doc:"http and loki: a bearer token, stored encrypted"`
}

// LogDrainTest is the outcome of sending a test line to a drain.
type LogDrainTest struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	Drain LogDrain `json:"drain"`
}
