// Package pgready asks a PostgreSQL server whether it accepts
// connections, the way pg_isready does: it sends a startup message and
// reads the first answer. A server that is starting up, shutting down or
// replaying its WAL after a crash or a restore listens on its port and
// answers every connection with the error 57P03 (cannot_connect_now), so
// a TCP connect alone calls it ready too early.
//
// dock-probe (the ongoing check, in the container) and the Docker runtime
// (a deploy's check, on the environment network) both use it. It imports
// only errors, io and net, so dock-probe stays small.
package pgready

import (
	"errors"
	"io"
	"net"
)

// maxMessage caps the answer read from the server: a real first answer
// is well under a kilobyte.
const maxMessage = 64 << 10

// notReady is the SQLSTATE of cannot_connect_now.
const notReady = "57P03"

// startup is a protocol 3.0 StartupMessage for user and database
// postgres: its length, the version 196608 and the parameters. Neither
// has to exist: the server says it can't take connections before it
// looks at them.
var startup = func() []byte {
	params := "user\x00postgres\x00database\x00postgres\x00\x00"
	n := 8 + len(params)
	return append([]byte{0, 0, 0, byte(n), 0, 3, 0, 0}, params...)
}()

// ErrNotPostgres is Check's error for an answer that isn't a PostgreSQL
// message.
var ErrNotPostgres = errors.New("the answer is not PostgreSQL's")

// NotReadyError is Check's error for a server that answers but takes no
// connections yet. Message is the server's own reason, such as "the
// database system is starting up".
type NotReadyError struct{ Message string }

// Error implements error.
func (e *NotReadyError) Error() string {
	if e.Message == "" {
		return "PostgreSQL is not accepting connections yet"
	}
	return "PostgreSQL is not accepting connections yet: " + e.Message
}

// Check sends a startup message on conn and reads the first answer. It
// returns nil when the server accepts connections: it asks for
// authentication, or refuses this login for any reason of its own (a
// wrong password, no such database or role). It returns a *NotReadyError
// when the server answers 57P03, and the read or write error when the
// connection ends or times out before a whole message arrived
// (io.ErrUnexpectedEOF for one cut short). The caller sets conn's
// deadline and closes it.
func Check(conn net.Conn) error {
	if _, err := conn.Write(startup); err != nil {
		return err
	}
	var head [5]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return eof(err)
	}
	n := int(head[1])<<24 | int(head[2])<<16 | int(head[3])<<8 | int(head[4])
	if n < 4 || n > maxMessage {
		return ErrNotPostgres
	}
	body := make([]byte, n-4)
	if _, err := io.ReadFull(conn, body); err != nil {
		return eof(err)
	}
	if head[0] != 'E' {
		return nil
	}
	// An ErrorResponse is fields of one type byte and a zero-terminated
	// string, ended by a zero byte: C is the SQLSTATE, M the message.
	var code, message string
	for len(body) > 1 {
		end := 1
		for end < len(body) && body[end] != 0 {
			end++
		}
		switch body[0] {
		case 'C':
			code = string(body[1:end])
		case 'M':
			message = string(body[1:end])
		}
		if end == len(body) {
			break
		}
		body = body[end+1:]
	}
	if code == notReady {
		return &NotReadyError{Message: message}
	}
	return nil
}

// eof makes a connection closed before any answer a cut-short one too.
func eof(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
