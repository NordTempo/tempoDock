package remote

import (
	"context"
	"fmt"
	"net/http"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Exec over the node link (`dock exec`, ADR 0111), protocol 39: one
// full-duplex call per session, framed like run-job-stream (streamCall,
// serveStream). Canceling resets the call, which ends the session on the
// node and stops what it left running there.

// ExecProtocol is the first node protocol that serves exec.
const ExecProtocol = 39

// execRequest is exec's argument.
type execRequest struct {
	ContainerID string     `json:"container_id"`
	Labels      wireLabels `json:"labels"`
	Cmd         []string   `json:"cmd"`
	Session     string     `json:"session"`
	TTY         bool       `json:"tty,omitempty"`
	Cols        uint16     `json:"cols,omitempty"`
	Rows        uint16     `json:"rows,omitempty"`
	Stdin       bool       `json:"stdin,omitempty"`
}

// Exec implements node.Containers on a remote server.
func (c *Client) Exec(ctx context.Context, req node.ExecRequest) (int, error) {
	// One session for the check and the call: a node that reconnected in
	// between on an older dock would get a call it has no route for.
	s, err := c.session(ctx)
	if err != nil {
		return 0, err
	}
	if s.hello.Protocol < ExecProtocol {
		return 0, fmt.Errorf("server %s runs dock %s, which can't open a shell in a container (`dock exec`); upgrade dock on it", s.name(), s.hello.Version)
	}
	st := req.Stream
	args := execRequest{
		ContainerID: req.ContainerID, Labels: toWireLabels(req.Labels), Cmd: req.Cmd, Session: req.Session,
		TTY: st.TTY, Cols: st.Size.Cols, Rows: st.Size.Rows, Stdin: st.Stdin != nil,
	}
	return streamCall(ctx, s, opExec, args, st, nil)
}

// serveExec serves exec on the node.
func serveExec(rt node.Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, up, err := readArgs[execRequest](r)
		if err != nil {
			newResponder(w).fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		serveStream(w, r, up, a.TTY, a.Cols, a.Rows, a.Stdin, func(st *node.JobStream) (int, error) {
			return rt.Exec(r.Context(), node.ExecRequest{ContainerID: a.ContainerID, Labels: a.Labels.node(), Cmd: a.Cmd, Session: a.Session, Stream: st})
		})
	}
}
