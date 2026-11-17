package docker

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// lifecycleActions are the container events worth reporting. Exec, attach,
// resize and similar noise (including the health probe's exec_start and
// exec_die) is filtered out by Docker. "health_status" matches Docker's
// "health_status: healthy" and "health_status: unhealthy", sent when a
// container's health check changes state (ADR 0043).
var lifecycleActions = []string{
	"create", "start", "restart", "stop", "kill", "die", "oom",
	"pause", "unpause", "destroy", string(events.ActionHealthStatus),
}

// sidecarActions are the events of sidecars Events passes on: whether
// one runs and how its health check does.
var sidecarActions = map[string]bool{"start": true, "die": true, string(events.ActionHealthStatus): true}

const (
	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second
)

// Events streams lifecycle events of managed containers until ctx ends.
// After a dropped connection it reconnects with backoff and resumes from the
// last event seen. Events Docker no longer buffers (e.g. across a daemon
// restart) are lost, so consumers should reconcile with List on reconnect.
func (r *Runtime) Events(ctx context.Context) (<-chan node.ContainerEvent, error) {
	ch := make(chan node.ContainerEvent)
	go func() {
		defer close(ch)
		filters := managedFilter().Add("type", "container").Add("event", lifecycleActions...)
		var since time.Time
		backoff := minBackoff
		for {
			opts := client.EventsListOptions{Filters: filters}
			if !since.IsZero() {
				// Resume just after the last event so nothing is repeated.
				next := since.Add(time.Nanosecond)
				opts.Since = fmt.Sprintf("%d.%09d", next.Unix(), next.Nanosecond())
			}
			res := r.cli.Events(ctx, opts)
		stream:
			for {
				select {
				case msg := <-res.Messages:
					ev := toEvent(msg)
					since, backoff = ev.Time, minBackoff
					if !isApp(msg.Actor.Attributes) {
						if !isSidecar(msg.Actor.Attributes) || !sidecarActions[ev.Action] {
							continue
						}
						ev.Role = node.RoleSidecar
					}
					select {
					case ch <- ev:
					case <-ctx.Done():
						return
					}
				case err := <-res.Err:
					if ctx.Err() != nil {
						return
					}
					slog.Warn("docker event stream failed, reconnecting", "err", err, "retry_in", backoff)
					break stream
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-time.After(backoff):
				backoff = min(backoff*2, maxBackoff)
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// toEvent maps a Docker container event to node.ContainerEvent. Docker puts
// the container's labels and, for die events, the exit code in Attributes.
// A "health_status: <state>" action becomes "health_status" with Health
// set to the state.
func toEvent(msg events.Message) node.ContainerEvent {
	ev := node.ContainerEvent{
		ContainerID: msg.Actor.ID,
		Labels:      node.LabelsFrom(msg.Actor.Attributes),
		Action:      string(msg.Action),
		Time:        time.Unix(0, msg.TimeNano),
	}
	if msg.TimeNano == 0 {
		ev.Time = time.Unix(msg.Time, 0)
	}
	if state, ok := strings.CutPrefix(ev.Action, string(events.ActionHealthStatus)+":"); ok {
		ev.Action, ev.Health = string(events.ActionHealthStatus), strings.TrimSpace(state)
	}
	if code, ok := msg.Actor.Attributes["exitCode"]; ok {
		ev.ExitCode, _ = strconv.Atoi(code)
	}
	return ev
}
