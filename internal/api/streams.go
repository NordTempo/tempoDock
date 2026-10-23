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

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// heartbeat keeps idle SSE connections (and proxies) alive.
const heartbeat = 25 * time.Second

// sseWriteTimeout bounds each write to an SSE client, so a client that stops
// reading but keeps its connection open can't hold a stream forever.
const sseWriteTimeout = 15 * time.Second

// SSE streams start with a `ready` event once the subscription is live
// (anything published after it is delivered), and end with an `end` event,
// or an `error` event carrying an apitypes.Error if the stream failed after
// it started. Errors before the stream starts (auth, not found) are normal
// problem responses.
const sseDoc = "\n\nServer-Sent Events. A first `ready` event means the stream is live: anything that happens after it is delivered. A final `end` event marks a clean end of the stream; an `error` event carries a problem document. Comment lines are heartbeats."

// sseFormat says how a stream's items are written: the `event:` name, an
// optional `id:` and the `data:` JSON.
type sseFormat[T any] struct {
	name func(T) string
	// id is nil for streams without ids.
	id func(T) string
	// data encodes an item; nil means json.Marshal of the item. schema is
	// the documented data type when data encodes something else (nil: T).
	data   func(T) ([]byte, error)
	schema reflect.Type
}

// sse registers a Server-Sent Events operation; events documents the
// possible `event:` names.
func sse[I, T any](s *Server, a huma.API, op huma.Operation, events []string, f sseFormat[T], h func(context.Context, core.Actor, *I) (core.Stream[T], error)) {
	if f.schema == nil {
		f.schema = reflect.TypeFor[T]()
	}
	if f.data == nil {
		f.data = func(v T) ([]byte, error) { return json.Marshal(v) }
	}
	data := a.OpenAPI().Components.Schemas.Schema(f.schema, true, "")
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
			err := stream(ctx, w.ready, func(items ...T) error {
				batch := make([]sseEvent, len(items))
				for i, v := range items {
					b, err := f.data(v)
					if err != nil {
						return err
					}
					batch[i] = sseEvent{name: f.name(v), data: b}
					if f.id != nil {
						batch[i].id = f.id(v)
					}
				}
				return w.write(batch...)
			})
			switch {
			case err != nil && !w.started():
				w.problem(s.problem(err))
			case err != nil && ctx.Err() == nil:
				b, _ := json.Marshal(s.problem(err))
				_ = w.write(sseEvent{name: "error", data: b})
			default:
				if w.write(sseEvent{name: "end", data: []byte("{}")}) == nil {
					w.clearDeadline()
				}
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

// sseEvent is one encoded event.
type sseEvent struct {
	name, id string
	data     []byte
}

// sseWriter writes an event stream. Headers go out with the `ready` event
// (or the first event), so a stream that fails before it is live can still
// answer with a plain problem response. Each write is one batch of events
// and one flush, with a write deadline.
type sseWriter struct {
	mu   sync.Mutex
	hctx huma.Context
	w    io.Writer // nil until the stream starts
	rc   *http.ResponseController
	buf  []byte
}

func (w *sseWriter) started() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w != nil
}

// ready starts the stream and tells the client it is live.
func (w *sseWriter) ready() { _ = w.write() }

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
	if rw, ok := w.w.(http.ResponseWriter); ok {
		w.rc = http.NewResponseController(rw)
	}
	return w.sendLocked([]byte("event: ready\ndata: {}\n\n"))
}

// write starts the stream if needed and writes events with one flush.
func (w *sseWriter) write(events ...sseEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.startLocked(); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	w.buf = w.buf[:0]
	for _, e := range events {
		if e.id != "" {
			w.buf = append(append(append(w.buf, "id: "...), e.id...), '\n')
		}
		w.buf = append(append(append(w.buf, "event: "...), e.name...), "\ndata: "...)
		w.buf = append(append(w.buf, e.data...), "\n\n"...)
	}
	return w.sendLocked(w.buf)
}

// sendLocked writes and flushes b, giving up after sseWriteTimeout.
func (w *sseWriter) sendLocked(b []byte) error {
	if w.rc == nil {
		_, err := w.w.Write(b)
		return err
	}
	if err := w.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := w.w.Write(b); err != nil {
		return err
	}
	if err := w.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// clearDeadline lifts the write deadline after a clean end, so it doesn't
// cut off a later response on the same connection.
func (w *sseWriter) clearDeadline() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rc != nil {
		_ = w.rc.SetWriteDeadline(time.Time{})
	}
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
				_ = w.sendLocked([]byte(": ping\n\n"))
			}
			w.mu.Unlock()
		}
	}
}

// eventID is a live event's SSE `id:`: the release event sequence, so
// clients can de-duplicate against GET release history.
func eventID(e core.LiveEvent) string {
	if e.ReleaseEvent != nil {
		return strconv.FormatInt(e.ReleaseEvent.Seq, 10)
	}
	return ""
}

func registerStreams(s *Server, a huma.API) {
	eventNames := []string{
		string(apitypes.EventRelease), string(apitypes.EventReleaseEvent), string(apitypes.EventBuildLog),
		string(apitypes.EventServiceStatus), string(apitypes.EventDomain), string(apitypes.EventAudit),
		string(apitypes.EventDrift), string(apitypes.EventLagged), string(apitypes.EventUsage),
		string(apitypes.EventProjectRenamed),
	}
	sse(s, a, huma.Operation{
		OperationID: "events", Method: http.MethodGet, Path: Prefix + "/events",
		Summary: "Live events", Tags: []string{"events"},
		Description: "Release progress, build logs, service status, domain status, audit events and each minute's resource usage the caller may see (server usage and platform events: operators only). " +
			"The `event:` name equals the payload's `type`. After a `lagged` event the client missed events and should refetch.",
	}, eventNames, sseFormat[core.LiveEvent]{
		name: func(e core.LiveEvent) string { return string(e.Type) }, id: eventID,
		// Encoded once per event, not once per client.
		data: core.LiveEvent.JSON, schema: reflect.TypeFor[apitypes.Event](),
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Project     string `query:"project"`
		Environment string `query:"environment" doc:"Requires project"`
		Service     string `query:"service" doc:"Requires environment"`
	}) (core.Stream[core.LiveEvent], error) {
		return s.core.Events(ctx, actor, core.EventFilter{Project: in.Project, Environment: in.Environment, Service: in.Service})
	})

	sse(s, a, huma.Operation{
		OperationID: "follow-build-log", Method: http.MethodGet, Path: releasePathT + "/build-log/stream",
		Summary: "Follow a build log", Tags: []string{"releases"},
		Description: "The build log so far, then new lines until the build phase ends.",
	}, []string{"line"}, sseFormat[apitypes.BuildLogLine]{
		name: func(apitypes.BuildLogLine) string { return "line" },
		id:   func(l apitypes.BuildLogLine) string { return strconv.Itoa(l.Seq) },
	}, func(ctx context.Context, actor core.Actor, in *ReleasePath) (core.Stream[apitypes.BuildLogLine], error) {
		return s.core.FollowBuildLog(ctx, actor, in.ref(), in.Number)
	})

	sse(s, a, huma.Operation{
		OperationID: "service-logs", Method: http.MethodGet, Path: servicePathT + "/logs",
		Summary: "Service logs", Tags: []string{"services"},
		Description: "Output of the active release's container (or `release`), read from the runtime. For a failed release, the last 200 lines its container wrote before it was removed.",
	}, []string{"line"}, sseFormat[apitypes.LogLine]{
		name: func(apitypes.LogLine) string { return "line" },
	}, func(ctx context.Context, actor core.Actor, in *struct {
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
