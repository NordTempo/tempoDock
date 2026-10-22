// Package remote carries node.Runtime calls between the control plane and a
// remote server, over one long-lived connection that the node dials out
// (ADR 0014).
//
// Layers, bottom up:
//
//	TCP, dialed by the node
//	TLS 1.3, both sides pinned by key fingerprint, ALPN "dock-node/1"
//	  (or "dock-join/1" for the one connection that joins a node)
//	HTTP/2 with the roles reversed: the control plane is the HTTP client,
//	  the node serves (it never listens on the network)
//	one POST per Runtime call; requests and responses are JSON lines of the
//	  wire types in wire.go, except Dial, whose bodies carry raw bytes
//
// Everything below the frames is the standard library (crypto/tls and
// net/http's HTTP/2), which the binary already links.
//
// The control plane runs a Hub (hub.go), which hands core a Client per
// server (client.go). A server runs a Node (node.go) after joining once with
// Join (join.go), and serves the control plane's registry on its own
// loopback through the link (registry.go).
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	// ALPN carries the protocol's major version. A node and control plane
	// without a common one fail the TLS handshake. When it becomes
	// dock-node/2, the control plane keeps accepting dock-node/1 for one
	// release cycle (acceptedALPN).
	ALPN = "dock-node/1"
	// joinALPN marks the one connection that joins a new node: an unknown
	// key is allowed, and the control plane only asks for the join secret.
	joinALPN = "dock-join/1"

	// Protocol is the minor version, exchanged in hello. Within a major
	// version, changes are additive only: new ops, new optional fields.
	// 2 added run-job (ADR 0019) and the health check's port.
	Protocol = 2
	// MinProtocol is the oldest minor version this control plane drives.
	// Older nodes stay connected for hello only (StatusIncompatible).
	MinProtocol = 1

	// DefaultKeepAlive is how long a connection may be silent before an
	// HTTP/2 ping checks it; an unanswered ping closes it after as long again.
	DefaultKeepAlive = 15 * time.Second
)

// acceptedALPN are the major versions the control plane speaks, newest
// first.
var acceptedALPN = []string{ALPN}

// Size limits. The control plane trusts nodes less than itself (ADR 0014,
// compromised node), so it caps everything a node sends: no message makes
// it buffer more than maxMessage. Streams (logs, events, build output) are
// unbounded in total but flow through one bounded message at a time, and
// Dial's raw bytes are bounded by HTTP/2 flow control.
const (
	// maxMessage caps one JSON line: request arguments, a response frame
	// or an error body.
	maxMessage = 1 << 20
	// maxLogChunk is the largest progress chunk in one log frame; the node
	// splits bigger writes. JSON escaping can grow it 6x, still well under
	// maxMessage.
	maxLogChunk = 64 << 10
	// maxLogText truncates one container log line.
	maxLogText = 64 << 10
	// maxErrorMessage truncates error messages that cross the link.
	maxErrorMessage = 4 << 10
	// maxHeaderBytes caps HTTP/2 headers in both directions.
	maxHeaderBytes = 64 << 10
	// maxListItems caps the elements of any list in a node's answer
	// (containers, IDs, samples, IPs). A frame under maxMessage can still
	// hold 300k empty objects, each a full struct once decoded; capped
	// lists reject such a message instead of allocating it.
	maxListItems = 10_000
)

var (
	// ErrOffline means the server isn't connected, or its connection dropped
	// while a call was in flight. Core maps it to server_offline.
	ErrOffline = errors.New("server is offline")
	// ErrIncompatible means the server is connected but speaks a protocol
	// version this control plane no longer drives. Core maps it to
	// version_incompatible.
	ErrIncompatible = errors.New("server runs an incompatible version of dock; upgrade dock on it")
	// errTooLarge is a message over maxMessage.
	errTooLarge = fmt.Errorf("message over the %d byte limit", maxMessage)
	// errTooMany is a list over maxListItems.
	errTooMany = fmt.Errorf("list over the %d item limit", maxListItems)
)

// Operations, served at POST /v1/<op>.
const (
	opHello         = "hello"
	opPull          = "pull"
	opHasImage      = "has-image"
	opRemoveImage   = "remove-image"
	opEnsureNetwork = "ensure-network"
	opEnsureVolume  = "ensure-volume"
	opStart         = "start"
	opStop          = "stop"
	opRemove        = "remove"
	opInspect       = "inspect"
	opList          = "list"
	opReleaseCtrs   = "release-containers"
	opHealthCheck   = "health-check"
	opLogs          = "logs"
	opEvents        = "events"
	opBuild         = "build"
	opApplyRoutes   = "apply-routes"
	opCheckDomain   = "check-domain"
	opHostStats     = "host-stats"
	opCtrStats      = "container-stats"
	opDial          = "dial"    // full duplex, raw bytes: see dial.go
	opRunJob        = "run-job" // protocol 2
	opRotate        = "rotate"
	// Joining mode (join.go) only.
	opJoin   = "join"
	opJoined = "joined"
)

// frame is one line of a 200 response body. A response is any number of
// log or item frames followed by exactly one end frame; a body that stops
// without one means the connection was lost.
type frame struct {
	// Log is a chunk of progress output (Pull, Build).
	Log string `json:"log,omitempty"`
	// Item is one element of a stream (Logs, Events).
	Item json.RawMessage `json:"item,omitempty"`
	End  bool            `json:"end,omitempty"`
	// Result (on success) or Error (on failure) come with End.
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a runtime error that crossed the connection. errors.Is matches
// node.ErrNotFound and the context errors through its code.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	codeNotFound   = "not_found"
	codeCanceled   = "canceled"
	codeDeadline   = "deadline_exceeded"
	codeBadRequest = "bad_request"
	codeInternal   = "internal"
)

func (e *Error) Error() string { return e.Message }

// Is lets errors.Is see the node-side cause.
func (e *Error) Is(target error) bool {
	switch e.Code {
	case codeNotFound:
		return target == node.ErrNotFound
	case codeCanceled:
		return target == context.Canceled
	case codeDeadline:
		return target == context.DeadlineExceeded
	}
	return false
}

// wireError classifies a node-side error for the control plane.
func wireError(err error) *Error {
	code := codeInternal
	switch {
	case errors.Is(err, node.ErrNotFound):
		code = codeNotFound
	case errors.Is(err, context.Canceled):
		code = codeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codeDeadline
	}
	return &Error{Code: code, Message: truncate(err.Error(), maxErrorMessage)}
}

// truncate cuts s to at most n bytes, on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// lineReader reads JSON lines of at most maxMessage bytes, so a peer can't
// make the reader buffer without bound.
type lineReader struct {
	br  *bufio.Reader
	buf []byte
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{br: bufio.NewReaderSize(r, 16<<10)}
}

// next returns the next line without its newline. The slice is only valid
// until the next call. A stream that ends cleanly between lines is io.EOF.
func (r *lineReader) next() ([]byte, error) {
	r.buf = r.buf[:0]
	for {
		chunk, err := r.br.ReadSlice('\n')
		if len(r.buf)+len(chunk) > maxMessage {
			return nil, errTooLarge
		}
		r.buf = append(r.buf, chunk...)
		switch {
		case err == nil:
			return r.buf[:len(r.buf)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(r.buf) > 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

// opaqueConn hides a *tls.Conn from net/http, so it speaks HTTP/2 with
// prior knowledge on it instead of looking at the TLS state (our ALPN is
// "dock-node/1", not "h2"). TLS is still there underneath.
type opaqueConn struct{ net.Conn }

// h2Only makes net/http speak HTTP/2 with prior knowledge and nothing else.
func h2Only() *http.Protocols {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &p
}

func or[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// trimJobOutput keeps the last maxLogChunk bytes of a job's output (on a
// rune boundary), so its frame stays under maxMessage however much JSON
// escaping grows it.
func trimJobOutput(s string) string {
	if len(s) <= maxLogChunk {
		return s
	}
	i := len(s) - maxLogChunk
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}
