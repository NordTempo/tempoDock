// Package remote carries node.Runtime calls between the control plane and a
// remote server, over one long-lived connection that the node dials out
// (ADR 0014). Spike: nothing imports it yet.
//
// Layers, bottom up:
//
//	TCP, dialed by the node
//	TLS 1.3, both sides pinned by key fingerprint, ALPN "dock-node/1"
//	HTTP/2 with the roles reversed: the control plane is the HTTP client,
//	  the node serves (it never listens on the network)
//	one POST per Runtime call; responses are NDJSON frames, except Dial,
//	  whose bodies carry raw bytes both ways
//
// Everything below the frames is the standard library (crypto/tls and
// net/http's HTTP/2), which the binary already links.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	// ALPN carries the protocol's major version. A node and control plane
	// without a common one fail the TLS handshake.
	ALPN = "dock-node/1"
	// Protocol is the minor version, exchanged in hello. Within a major
	// version, changes are additive only.
	Protocol = 1

	// DefaultKeepAlive is how long a connection may be silent before an
	// HTTP/2 ping checks it; an unanswered ping closes it after as long again.
	DefaultKeepAlive = 15 * time.Second
)

// ErrOffline means the server isn't connected, or its connection dropped
// while a call was in flight.
var ErrOffline = errors.New("server is offline")

// Info is what a node reports about itself when it connects.
type Info struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// Operations, served at POST /v1/<op>.
const (
	opHello         = "hello"
	opPull          = "pull"
	opHasImage      = "has-image"
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
	opDial          = "dial" // full duplex, raw bytes: see dial.go
)

// Request bodies start with one line of JSON arguments. Build follows it
// with the raw build context.
type (
	noArgs   struct{}
	refArgs  struct{ Ref string }
	pullArgs struct {
		Ref  string
		Opts node.PullOptions
	}
	nameArgs    struct{ Name string }
	idArgs      struct{ ID string }
	releaseArgs struct{ Release int64 }
	idsArgs     struct{ IDs []string }
	labeledArgs struct {
		Name   string
		Labels node.Labels
	}
	stopArgs struct {
		ID      string
		Timeout time.Duration
	}
	healthArgs struct {
		Container node.Container
		Check     node.HealthCheck
	}
	logsArgs struct {
		ID   string
		Opts node.LogOptions
	}
	buildArgs struct {
		Dockerfile   string
		BuildCommand string
		StartCommand string
		BuildArgs    map[string]string
		CacheScope   string
		Tag          string
		Push         *node.PushTarget
	}
	routesArgs struct{ Routes []node.Route }
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
	return &Error{Code: code, Message: err.Error()}
}

// opaqueConn hides a *tls.Conn from net/http, so it speaks HTTP/2 with
// prior knowledge on it instead of looking at the TLS state (our ALPN is
// "dock-node/1", not "h2"). TLS is still there underneath.
type opaqueConn struct{ net.Conn }
