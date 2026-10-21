package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/NordTempo/platform/internal/node"
)

const (
	defaultInterval = time.Second
	probeTimeout    = 5 * time.Second
	// tcpSettle is how long a TCP probe waits for the connection to be
	// closed by docker-proxy, which accepts on the host port even when
	// nothing listens inside the container.
	tcpSettle = 300 * time.Millisecond
)

// HealthCheck waits until c passes hc (see node.HealthCheck). It fails fast
// if the container exits or restarts while being checked; c.RestartCount is
// the baseline for detecting restarts.
func (r *Runtime) HealthCheck(ctx context.Context, c node.Container, hc node.HealthCheck) error {
	if hc.Path != "" && c.HostPort == 0 {
		return errors.New("health check path is set but the container publishes no port")
	}
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
	evCtx, stopEvents := context.WithCancel(ctx)
	defer stopEvents()
	events := r.cli.Events(evCtx, client.EventsListOptions{Filters: client.Filters{}.
		Add("type", "container").
		Add("container", c.ID).
		Add("event", "die")})
	dies, evErrs := events.Messages, events.Err

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		st, err := r.inspect(ctx, c.ID)
		if err != nil {
			if ctx.Err() == nil {
				return err
			}
		} else {
			if err := crashed(st, c.RestartCount); err != nil {
				return err
			}
			lastErr = probe(ctx, toContainer(st), hc)
			if lastErr == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return parent.Err()
			}
			if lastErr != nil {
				return fmt.Errorf("health check timed out after %s: %w", hc.Timeout, lastErr)
			}
			return fmt.Errorf("health check timed out after %s", hc.Timeout)
		case ev := <-dies:
			code, _ := strconv.Atoi(ev.Actor.Attributes["exitCode"])
			st, err := r.inspect(ctx, c.ID)
			return errExited(code, err == nil && st.State != nil && st.State.OOMKilled)
		case err := <-evErrs:
			// Polling still catches crashes via state and restart count,
			// just less promptly.
			if ctx.Err() == nil {
				slog.Debug("health check event stream failed", "container", c.ID, "err", err)
			}
			dies, evErrs = nil, nil
		case <-ticker.C:
		}
	}
}

// crashed reports whether st shows the container died or restarted since
// the baseline restart count.
func crashed(st container.InspectResponse, baseRestarts int) error {
	s := st.State
	if s == nil {
		return nil
	}
	switch s.Status {
	case container.StateExited, container.StateDead, container.StateRestarting:
		return errExited(s.ExitCode, s.OOMKilled)
	case container.StateRemoving:
		return errors.New("container was removed during health check")
	}
	if st.RestartCount > baseRestarts {
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
