package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	defaultInterval = time.Second
	probeTimeout    = 5 * time.Second
	// tcpSettle is how long a TCP probe waits for the connection to be
	// closed by docker-proxy, which accepts on the host port even when
	// nothing listens inside the container. It closes within a millisecond
	// (0.1-0.4 ms measured), so 50 ms leaves wide headroom.
	tcpSettle = 50 * time.Millisecond
)

// probeBackoff are the waits between the first probes: most apps listen
// within a few hundred milliseconds of starting, and every deploy waits
// for the probe that sees it. After these, probes run every interval.
var probeBackoff = [...]time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}

// probeDelay is the wait after the n-th (zero-based) failed probe.
func probeDelay(n int, interval time.Duration) time.Duration {
	if n < len(probeBackoff) {
		return min(probeBackoff[n], interval)
	}
	return interval
}

// HealthCheck waits until c passes hc (see node.HealthCheck). It probes at
// once, then backs off from 50 ms to hc.Interval (probeBackoff). It fails fast
// if the container exits or restarts while being checked; c.RestartCount is
// the baseline for detecting restarts. The container's state is checked
// before anything else, so an app that exits right away is reported as
// exited, not as missing a port (an exited container publishes none).
func (r *Runtime) HealthCheck(ctx context.Context, c node.Container, hc node.HealthCheck) error {
	parent := ctx
	if hc.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, hc.Timeout)
		defer cancel()
	}
	interval := hc.Interval
	if interval <= 0 {
		interval = defaultInterval
	}

	// Die events make crashes visible immediately and carry the exit code,
	// which Docker resets once the restart policy brings the container back.
	// OOM events are kept for the same reason: Docker clears OOMKilled when
	// the container starts again, often before the die can be inspected.
	// They are read from the container's start on: an app that exits at
	// once can die and be restarted before this subscribes.
	evCtx, stopEvents := context.WithCancel(ctx)
	defer stopEvents()
	var since string
	if !c.StartedAt.IsZero() {
		since = fmt.Sprintf("%d.%09d", c.StartedAt.Unix(), c.StartedAt.Nanosecond())
	}
	sub := r.cli.Events(evCtx, client.EventsListOptions{Since: since, Filters: client.Filters{}.
		Add("type", "container").
		Add("container", c.ID).
		Add("event", "die", "oom")})
	evs, evErrs := sub.Messages, sub.Err
	oom := false

	timer := time.NewTimer(interval)
	defer timer.Stop()

	var lastErr error
	for attempt := 0; ; attempt++ {
		st, err := r.inspect(ctx, c.ID)
		if err != nil {
			if ctx.Err() == nil {
				return err
			}
		} else {
			if err := crashed(st, c.RestartCount, oom); err != nil {
				// Restarted between two checks: the restart's events may
				// not be read yet. Its die carries the exit code (inspect
				// shows the new run), and an oom one says why it died.
				if errors.Is(err, errRestarted) && evs != nil {
					if code, died, oom := awaitDie(ctx, evs, oom); died || oom {
						return errExited(code, oom, memoryLimit(st))
					}
				}
				return err
			}
			cur := toContainer(st)
			if hc.Path != "" && cur.HostPort == 0 {
				return errors.New("health check path is set but the container publishes no port")
			}
			if hc.Path == "" && hc.Port != 0 {
				lastErr = r.probeNetwork(ctx, st, hc.Port)
			} else {
				lastErr = probe(ctx, cur, hc)
			}
			if lastErr == nil {
				return nil
			}
		}
		timer.Reset(probeDelay(attempt, interval))

		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return parent.Err()
			}
			if lastErr != nil {
				return fmt.Errorf("health check timed out after %s: %w", hc.Timeout, lastErr)
			}
			return fmt.Errorf("health check timed out after %s", hc.Timeout)
		case ev := <-evs:
			if ev.Action == "oom" {
				oom = true
				continue
			}
			code, _ := strconv.Atoi(ev.Actor.Attributes["exitCode"])
			st, err := r.inspect(ctx, c.ID)
			oom = oom || err == nil && st.State != nil && st.State.OOMKilled
			if !oom && code == oomExitCode {
				// Killed: the oom event may still be on its way.
				oom = awaitOOM(ctx, evs)
			}
			return errExited(code, oom, memoryLimit(st))
		case err := <-evErrs:
			// Polling still catches crashes via state and restart count,
			// just less promptly.
			if ctx.Err() == nil {
				slog.Debug("health check event stream failed", "container", c.ID, "err", err)
			}
			evs, evErrs = nil, nil
		case <-timer.C:
		}
	}
}

// oomExitCode is the exit code of a process killed by SIGKILL, which is how
// the kernel's OOM killer ends it.
const oomExitCode = 137

// awaitOOM waits briefly for an oom event after a die with oomExitCode,
// since Docker doesn't guarantee their order.
func awaitOOM(ctx context.Context, evs <-chan events.Message) bool {
	t := time.NewTimer(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case ev := <-evs:
			if ev.Action == "oom" {
				return true
			}
		case <-t.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// awaitDie waits briefly for the die event of a restart that inspect saw
// first and returns its exit code. oom says an oom event was already seen;
// one arriving now counts too.
func awaitDie(ctx context.Context, evs <-chan events.Message, oom bool) (code int, died, oomSeen bool) {
	t := time.NewTimer(500 * time.Millisecond)
	defer t.Stop()
	// A die with oomExitCode waits on for its oom event, as awaitOOM does.
	for !died || (!oom && code == oomExitCode) {
		select {
		case ev := <-evs:
			switch ev.Action {
			case "oom":
				oom = true
			case "die":
				code, _ = strconv.Atoi(ev.Actor.Attributes["exitCode"])
				died = true
			}
		case <-t.C:
			return code, died, oom
		case <-ctx.Done():
			return code, died, oom
		}
	}
	return code, died, oom
}

// memoryLimit is a container's memory limit in bytes (0: none or unknown).
func memoryLimit(st container.InspectResponse) int64 {
	if st.HostConfig == nil {
		return 0
	}
	return st.HostConfig.Memory
}

// crashed reports whether st shows the container died or restarted since
// the baseline restart count. oom says an oom event was seen.
func crashed(st container.InspectResponse, baseRestarts int, oom bool) error {
	s := st.State
	if s == nil {
		return nil
	}
	switch s.Status {
	case container.StateExited, container.StateDead, container.StateRestarting:
		return errExited(s.ExitCode, oom || s.OOMKilled, memoryLimit(st))
	case container.StateRemoving:
		return errors.New("container was removed during health check")
	}
	if st.RestartCount > baseRestarts {
		if oom {
			return errExited(oomExitCode, true, memoryLimit(st))
		}
		return errRestarted
	}
	return nil
}

// probe runs one check attempt against a running container.
func probe(ctx context.Context, c node.Container, hc node.HealthCheck) error {
	switch {
	case hc.Path != "":
		return probeHTTP(ctx, fmt.Sprintf("http://127.0.0.1:%d%s", c.HostPort, hc.Path))
	case c.HostPort != 0:
		return probeTCP(ctx, c.HostPort)
	default:
		if c.State != node.StateRunning {
			return fmt.Errorf("container is %s", c.State)
		}
		if up := time.Since(c.StartedAt); up < hc.Grace {
			return fmt.Errorf("container has been running for %s, needs %s", up.Round(time.Second), hc.Grace)
		}
		return nil
	}
}

// healthClient never follows redirects: a 3xx already counts as healthy.
var healthClient = &http.Client{
	Timeout: probeTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func probeHTTP(ctx context.Context, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := healthClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", target, unwrapURLError(err))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("GET %s returned %s", target, resp.Status)
	}
	return nil
}

// probeNetwork checks that something accepts TCP connections on port of the
// container, on its environment network (as Dial reaches it). Unlike the
// loopback probe there is no docker-proxy in between, so a connect is
// enough.
func (r *Runtime) probeNetwork(ctx context.Context, st container.InspectResponse, port int) error {
	addr, err := r.envAddr(ctx, st, node.LabelsFrom(st.Config.Labels))
	if err != nil {
		return err
	}
	target := netip.AddrPortFrom(addr, uint16(port)).String()
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return fmt.Errorf("connect to port %d: %w", port, err)
	}
	return conn.Close()
}

func probeTCP(ctx context.Context, port int) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	// docker-proxy closes the connection right away when nothing listens in
	// the container. A live server keeps it open (or sends a banner).
	_ = conn.SetReadDeadline(time.Now().Add(tcpSettle))
	if _, err := conn.Read(make([]byte, 1)); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("connect to %s: connection closed, is the app listening on the container port?", addr)
	}
	return nil
}

// unwrapURLError drops net/http's "Get \"url\": " prefix, since callers
// already say which URL failed.
func unwrapURLError(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}
