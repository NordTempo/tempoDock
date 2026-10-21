package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/core"
)

// heartbeat keeps idle SSE connections (and proxies) alive.
const heartbeat = 25 * time.Second

// SSE streams start with a `ready` event once the subscription is live
// (anything published after it is delivered), and end with an `end` event,
// or an `error` event carrying an apitypes.Error if the stream failed after
// it started. Errors before the stream starts (auth, not found) are normal
// problem responses.
const sseDoc = "\n\nServer-Sent Events. A first `ready` event means the stream is live: anything that happens after it is delivered. A final `end` event marks a clean end of the stream; an `error` event carries a problem document. Comment lines are heartbeats."

// sse registers a Server-Sent Events operation. name picks each item's
// `event:` field (events documents the possible names) and id its optional
// `id:` field.
func sse[I, T any](s *Server, a huma.API, op huma.Operation, events []string, name, id func(T) string, h func(context.Context, core.Actor, *I) (core.Stream[T], error)) {
	var zero T
	data := a.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(zero), true, "")
	op.Description += sseDoc
	op.Responses = map[string]*huma.Response{"200": {
		Description: "Event stream",
		Content: map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{
			Type: huma.TypeArray,
			Items: &huma.Schema{
				Type:     huma.TypeObject,
				Required: []string{"event", "data"},
				Properties: map[string]*huma.Schema{
					"event": {Type: huma.TypeString, Enum: toAny(append(events, "ready", "end", "error"))},
					"id":    {Type: huma.TypeString},
					"data":  data,
				},
			},
		}}},
	}}
	authed(s, a, op, func(ctx context.Context, actor core.Actor, in *I) (*huma.StreamResponse, error) {
		stream, err := h(ctx, actor, in)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			w := &sseWriter{hctx: hctx}
			ctx, cancel := context.WithCancel(hctx.Context())
			defer cancel()
			go w.heartbeats(ctx)
			err := stream(ctx, w.ready, func(v T) error { return w.event(name(v), id(v), v) })
			switch {
			case err != nil && !w.started():
				w.problem(s.problem(err))
			case err != nil && ctx.Err() == nil:
				_ = w.event("error", "", s.problem(err))
			default:
				_ = w.event("end", "", struct{}{})
			}
		}}, nil
	})
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// sseWriter writes an event stream. Headers go out with the `ready` event
// (or the first event), so a stream that fails before it is live can still
// answer with a plain problem response.
type sseWriter struct {
	mu    sync.Mutex
	hctx  huma.Context
	w     io.Writer // nil until the stream starts
	flush func()
}

func (w *sseWriter) started() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w != nil
}

// ready starts the stream and tells the client it is live.
func (w *sseWriter) ready() { _ = w.event("ready", "", struct{}{}) }

// startLocked writes the headers and the ready event, once.
func (w *sseWriter) startLocked() error {
	if w.w != nil {
		return nil
	}
	w.hctx.SetHeader("Content-Type", "text/event-stream")
	w.hctx.SetHeader("Cache-Control", "no-cache")
	w.hctx.SetHeader("X-Accel-Buffering", "no")
	w.hctx.SetStatus(http.StatusOK)
	w.w = w.hctx.BodyWriter()
	w.flush = func() {}
	if f, ok := w.w.(http.Flusher); ok {
		w.flush = f.Flush
	}
	_, err := io.WriteString(w.w, "event: ready\ndata: {}\n\n")
	return err
}

func (w *sseWriter) event(name, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.startLocked(); err != nil {
		return err
	}
	if name != "ready" {
		if id != "" {
			if _, err := fmt.Fprintf(w.w, "id: %s\n", id); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
			return err
		}
	}
	w.flush()
	return nil
}

// problem answers a stream that failed before it started with a normal
// problem response.
func (w *sseWriter) problem(err error) {
	ae, ok := errors.AsType[*apitypes.Error](err)
	if !ok {
		ae = &apitypes.Error{Status: http.StatusInternalServerError, Title: "Internal Server Error", Code: apitypes.CodeInternal, Detail: err.Error()}
	}
	w.hctx.SetHeader("Content-Type", "application/problem+json")
	w.hctx.SetStatus(ae.Status)
	_ = json.NewEncoder(w.hctx.BodyWriter()).Encode(ae)
}

func (w *sseWriter) heartbeats(ctx context.Context) {
	t := time.NewTicker(heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.mu.Lock()
			if w.w != nil {
				_, _ = io.WriteString(w.w, ": ping\n\n")
				w.flush()
			}
			w.mu.Unlock()
		}
	}
}

// eventID is a live event's SSE `id:`: the release event sequence, so
// clients can de-duplicate against GET release history.
func eventID(e apitypes.Event) string {
	if e.ReleaseEvent != nil {
		return strconv.FormatInt(e.ReleaseEvent.Seq, 10)
	}
	return ""
}

func registerStreams(s *Server, a huma.API) {
	eventNames := []string{
		string(apitypes.EventRelease), string(apitypes.EventReleaseEvent), string(apitypes.EventBuildLog),
		string(apitypes.EventServiceStatus), string(apitypes.EventDomain), string(apitypes.EventAudit),
		string(apitypes.EventDrift), string(apitypes.EventLagged),
	}
	sse(s, a, huma.Operation{
		OperationID: "events", Method: http.MethodGet, Path: Prefix + "/events",
		Summary: "Live events", Tags: []string{"events"},
		Description: "Release progress, build logs, service status, domain status and audit events the caller may see. " +
			"The `event:` name equals the payload's `type`. After a `lagged` event the client missed events and should refetch.",
	}, eventNames, func(e apitypes.Event) string { return string(e.Type) }, eventID,
		func(ctx context.Context, actor core.Actor, in *struct {
			Project     string `query:"project"`
			Environment string `query:"environment" doc:"Requires project"`
			Service     string `query:"service" doc:"Requires environment"`
		}) (core.Stream[apitypes.Event], error) {
			return s.core.Events(ctx, actor, core.EventFilter{Project: in.Project, Environment: in.Environment, Service: in.Service})
		})

	sse(s, a, huma.Operation{
		OperationID: "follow-build-log", Method: http.MethodGet, Path: releasePathT + "/build-log/stream",
		Summary: "Follow a build log", Tags: []string{"releases"},
		Description: "The build log so far, then new lines until the build phase ends.",
	}, []string{"line"}, func(apitypes.BuildLogLine) string { return "line" },
		func(l apitypes.BuildLogLine) string { return strconv.Itoa(l.Seq) },
		func(ctx context.Context, actor core.Actor, in *ReleasePath) (core.Stream[apitypes.BuildLogLine], error) {
			return s.core.FollowBuildLog(ctx, actor, in.ref(), in.Number)
		})

	sse(s, a, huma.Operation{
		OperationID: "service-logs", Method: http.MethodGet, Path: servicePathT + "/logs",
		Summary: "Service logs", Tags: []string{"services"},
		Description: "Output of the active release's container (or `release`), read from the runtime.",
	}, []string{"line"}, func(apitypes.LogLine) string { return "line" }, func(apitypes.LogLine) string { return "" },
		func(ctx context.Context, actor core.Actor, in *struct {
			ServicePath
			Release int64  `query:"release" doc:"Release number; default the active release"`
			Follow  bool   `query:"follow" doc:"Keep streaming new lines"`
			Tail    int    `query:"tail" default:"100" minimum:"0" doc:"Last N lines of backlog; 0 for all"`
			Since   string `query:"since" doc:"RFC 3339 time or a duration like 10m"`
		}) (core.Stream[apitypes.LogLine], error) {
			q := core.LogQuery{Release: in.Release, Follow: in.Follow, Tail: in.Tail}
			if in.Since != "" {
				if d, err := time.ParseDuration(in.Since); err == nil {
					q.Since = time.Now().Add(-d)
				} else if t, err := time.Parse(time.RFC3339, in.Since); err == nil {
					q.Since = t
				} else {
					return nil, &apitypes.Error{Status: http.StatusBadRequest, Title: "Bad Request", Code: apitypes.CodeValidation,
						Detail: fmt.Sprintf("since %q must be an RFC 3339 time or a duration like 10m", in.Since)}
				}
			}
			return s.core.ServiceLogs(ctx, actor, in.ref(), q)
		})
}
