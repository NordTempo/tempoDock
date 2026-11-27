package apitypes

import "time"

// Shells in a service's running container (`dock exec`, ADR 0111).

// ExecProtocol is the Upgrade token of an exec session: `GET .../exec`
// with `Connection: Upgrade` and `Upgrade: dock-exec`, then runframe
// frames both ways, as for RunProtocol.
const ExecProtocol = "dock-exec"

// ExecEnd is an exec session's Exit frame: the command's exit code, or
// why it didn't get one.
type ExecEnd struct {
	// ExitCode is the command's, when it exited.
	ExitCode *int `json:"exit_code,omitempty"`
	// Error says why the session ended without an exit code, Code is its
	// error code when it has one (conflict: the service isn't running;
	// server_offline; forbidden: the caller's access changed).
	Code  ErrorCode `json:"code,omitempty"`
	Error string    `json:"error,omitempty"`
	// Container is the short ID of the container it ran in, Release the
	// release that container runs.
	Container string `json:"container"`
	Release   int64  `json:"release"`
}

// ExecTicket lets a browser open an exec session's WebSocket: `GET
// .../exec/ws` with the subprotocols `dock-exec` and the ticket. It is
// good once, for ExecTicketSeconds, for the person and service it was
// made for.
type ExecTicket struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ExecTicketSeconds is how long an ExecTicket is good for.
const ExecTicketSeconds = 30
