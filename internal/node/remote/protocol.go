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
	// 2 added run-job (ADR 0019) and the health check's port; 3 made
	// prune-images only remove the platform's own images and added
	// remove-network (ADR 0020 amendment); 4 added update and the
	// node's arch and last update in hello (ADR 0028); 5 added access
	// protection to routes (ADR 0030); 6 added drain-traffic and the
	// routes' traffic flag (ADR 0033); 7 added disk-usage (ADR 0035); 8
	// added ensure-tunnel and the node's Caddy HTTP port in hello (ADR
	// 0036); 9 added a direct server's tunnel listener: its port in hello
	// and tunnel-only routes (ADR 0036).
	// 10 added set-analytics, tail-spool, ack-spool, ensure-analytics-store,
	// dial-analytics and the routes' analytics hosts (ADR 0037); 11 runs
	// the store from the release's binary: run-analytics-store,
	// put-analytics-binary and remove-analytics-store, and answers
	// ensure-analytics-store (an image) with an error (ADR 0037); 12 adds
	// the replay spool: tail-spool's and ack-spool's spool, and the site
	// rules' replay settings (ADR 0037, A3). 13 added restart and the
	// ongoing health check: the container spec's health, a container's
	// health state and health_status events (ADR 0043); 14 added
	// archive-logs, archived-logs and prune-logs (ADR 0046).
	// 15 adds run-job's pause: containers frozen while the job runs (ADR
	// 0047); 16 adds run-job-stream, jobs connected to their caller
	// (`dock run`, ADR 0048); 17 adds the routes' stopped service,
	// answered with a 503 page (ADR 0050); 18 adds the pull's registry
	// username and password (ADR 0051); 19 added probe (ADR 0055); 20
	// adds remove-volume, for a closed preview's volumes (ADR 0056); 21
	// adds run-job's sync (dock-sync mounted in the job), the routes'
	// hold, and forward-listen and forward-accept, connections a server
	// forwards to the server its environment moved to (ADR 0063). 22 adds
	// resume, open-requests, the routes' wake and quiet probes, and wake
	// events: environments that sleep (ADR 0070, SleepProtocol). 23
	// adds scan-sbom, scan-match, scan-database and image-digest: image
	// vulnerability scanning on builders (ADR 0074, ScanProtocol). 24
	// adds the trace store: the kind of run-analytics-store,
	// remove-analytics-store and dial-analytics (TraceStoreProtocol). 25
	// adds the trace ingest: set-analytics' trace key and the spans spool
	// (TraceIngestProtocol). 26 adds the container spec's extra hosts and
	// Caddy's dock-otlp route: apps reach the trace ingest
	// (TraceAppsProtocol). 27 adds remote-digest, scan-database-import
	// and the build's base image (ADR 0074 phase 3, BaseProtocol). 28
	// adds the metrics spool: app metrics over OTLP (ADR 0075 phase 6,
	// MetricsProtocol). 29 adds the container spec's args: start commands
	// in exec form (ADR 0083, ExecFormProtocol). 30 sends a release's
	// signature with an update (ADR 0088, SignedUpdateProtocol). 31 adds
	// apply-routes' certificates: the wildcard of the server's base domain
	// (ADR 0090, WildcardProtocol). 32 adds warm-builder: builders fetch
	// Railpack's images ahead of their first build (ADR 0089 amendment,
	// WarmProtocol). 33 adds ensure-sidecar and remove-sidecar, and events
	// of sidecars with their role: a Postgres database's WAL archiver (ADR
	// 0093, SidecarProtocol), and run-job's dock: jobs that run the
	// node's own dock binary. 34 adds scan-database-prune, and prunes
	// and disk readings that know every image a release left, other
	// versions of the platform's own images, older rollbacks and the
	// vulnerability database's leftovers (ADR 0035 amendment 2,
	// PruneProtocol). 35 adds stop-analytics-store: analytics turned off
	// with its data kept (ADR 0101, StopStoreProtocol).
	Protocol = 35
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
	opPruneImages   = "prune-images"
	opPruneCache    = "prune-build-cache"
	opEnsureNetwork = "ensure-network"
	opRemoveNetwork = "remove-network" // protocol 3
	opEnsureVolume  = "ensure-volume"
	opRemoveVolume  = "remove-volume" // protocol 20
	opStart         = "start"
	opStop          = "stop"
	opRestart       = "restart"
	opRemove        = "remove"
	opInspect       = "inspect"
	opList          = "list"
	opReleaseCtrs   = "release-containers"
	opHealthCheck   = "health-check"
	opLogs          = "logs"
	opEvents        = "events"
	opBuild         = "build"
	opWarmBuilder   = "warm-builder"   // protocol 32
	opEnsureSidecar = "ensure-sidecar" // protocol 33
	opRemoveSidecar = "remove-sidecar" // protocol 33
	opApplyRoutes   = "apply-routes"
	opCheckDomain   = "check-domain"
	opHostStats     = "host-stats"
	opCtrStats      = "container-stats"
	opDrainTraffic  = "drain-traffic" // protocol 6
	opDiskUsage     = "disk-usage"    // protocol 7
	opEnsureTunnel  = "ensure-tunnel" // protocol 8
	opDial          = "dial"          // full duplex, raw bytes: see dial.go
	// Analytics (ADR 0037), protocol 10.
	opSetAnalytics    = "set-analytics"
	opTailSpool       = "tail-spool"
	opAckSpool        = "ack-spool"
	opEnsureAnalytics = "ensure-analytics-store"
	opDialAnalytics   = "dial-analytics" // raw bytes, like dial
	// Forwarding a moved environment's connections (ADR 0063), protocol
	// 21: see forward.go.
	opForwardListen = "forward-listen"
	opForwardAccept = "forward-accept" // raw bytes, like dial
	// Protocol 11.
	opRunAnalytics       = "run-analytics-store"
	opPutAnalyticsBinary = "put-analytics-binary"
	opRemoveAnalytics    = "remove-analytics-store"
	opStopAnalytics      = "stop-analytics-store" // protocol 35
	// Log history (ADR 0046), protocol 14.
	opArchiveLogs  = "archive-logs"
	opArchivedLogs = "archived-logs"
	opPruneLogs    = "prune-logs"
	opRunJob       = "run-job"        // protocol 2
	opRunJobStream = "run-job-stream" // protocol 16, full duplex: see job_stream.go
	opProbe        = "probe"          // protocol 19
	opResume       = "resume"         // protocol 22
	opOpenRequests = "open-requests"  // protocol 22
	opRotate       = "rotate"
	opUpdate       = "update" // protocol 4
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
// node.ErrNotFound, node.ErrNotRunning and the context errors through its
// code.
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
	// codeNoBinary is a node without the analytics binary it was asked
	// to run: the control plane sends it (put-analytics-binary).
	codeNoBinary = "no_analytics_binary"
	// codeNotRunning is Restart's node.ErrNotRunning (protocol 13).
	codeNotRunning = "not_running"
	// codeNoScanDatabase is Match's node.ErrNoScanDatabase (protocol 23).
	codeNoScanDatabase = "no_scan_database"
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
	case codeNoBinary:
		return target == errNoAnalyticsBinary
	case codeNotRunning:
		return target == node.ErrNotRunning
	case codeNoScanDatabase:
		return target == node.ErrNoScanDatabase
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
	case errors.Is(err, errNoAnalyticsBinary):
		code = codeNoBinary
	case errors.Is(err, node.ErrNotRunning):
		code = codeNotRunning
	case errors.Is(err, node.ErrNoScanDatabase):
		code = codeNoScanDatabase
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
