package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// defaultLogLines is how many lines of each log a failed release shows by
// default (`dock release show --log-lines`, and after a failed deploy).
const defaultLogLines = 40

// releaseView is a release as the CLI prints it: the API's release, plus
// the end of its logs when it failed. Its JSON is the release's with an
// added `logs` field.
type releaseView struct {
	apitypes.Release
	Logs *releaseLogs `json:"logs,omitempty"`
}

// releaseLogs are the last lines that explain a failed release, like the
// MCP tool release_details returns.
type releaseLogs struct {
	// Container is what the release's container wrote before it was
	// stopped (a crash or a failed health check), one "time [stderr] text"
	// string per line.
	Container []string `json:"container,omitempty"`
	// Build is the end of the build or image pull output.
	Build []string `json:"build,omitempty"`
}

// viewRelease is r with the last n lines of its logs when it failed (none
// for n 0 or a release that didn't fail). Logs are a help, not the result:
// one that can't be read is left out with a warning.
func (g *globals) viewRelease(ctx context.Context, s *session, r apitypes.Release, n int) releaseView {
	v := releaseView{Release: r}
	if r.Status != apitypes.ReleaseFailed || n <= 0 {
		return v
	}
	path := releaseTarget(r).servicePath()
	logs := &releaseLogs{}
	var err error
	// A release that never started a container has no output, and one
	// that deployed an image may have no build log: neither is an error.
	if logs.Container, err = containerTail(ctx, s, path, r.Number, n); err != nil && output.ExitCodeOf(err) != output.ExitNotFound {
		g.io.Warnf("Could not read the output of release %d: %v", r.Number, err)
	}
	if logs.Build, err = buildTail(ctx, s, path, r.Number, n); err != nil && output.ExitCodeOf(err) != output.ExitNotFound {
		g.io.Warnf("Could not read the build log of release %d: %v", r.Number, err)
	}
	if len(logs.Container)+len(logs.Build) > 0 {
		v.Logs = logs
	}
	return v
}

// containerTail reads the last n lines release number of the service at
// path wrote (`dock logs --release N`), keeping at most n in memory.
func containerTail(ctx context.Context, s *session, path string, number int64, n int) ([]string, error) {
	q := url.Values{"release": {itoa(number)}, "tail": {strconv.Itoa(n)}}
	out := make([]string, 0, n)
	err := apiclient.Follow(ctx, s.api, path+"/logs?"+q.Encode(), func(l apitypes.LogLine) error {
		if len(out) == n {
			out = append(out[:0], out[1:]...)
		}
		out = append(out, formatLogLine(l))
		return nil
	})
	return out, err
}

// formatLogLine is a line of service output as one string: "time text",
// with "[stderr]" before the text of stderr lines.
func formatLogLine(l apitypes.LogLine) string {
	var b strings.Builder
	b.WriteString(l.Time.UTC().Format(time.RFC3339))
	if l.Stream == "stderr" {
		b.WriteString(" [stderr]")
	}
	b.WriteByte(' ')
	b.WriteString(l.Text)
	return b.String()
}

// buildTail reads the last n lines of a release's build log (`dock logs
// --build --release N`), streaming it through with n lines in memory.
func buildTail(ctx context.Context, s *session, path string, number int64, n int) ([]string, error) {
	req, err := s.api.NewRequest(ctx, http.MethodGet, path+"/releases/"+itoa(number)+"/build-log", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.api.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return tailLines(resp.Body, n)
}

// tailLines returns the last n lines of r, keeping at most n lines in
// memory. A line over 64 KB ends the read there.
func tailLines(r io.Reader, n int) ([]string, error) {
	ring := make([]string, 0, n)
	next := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if len(ring) < n {
			ring = append(ring, line)
			continue
		}
		ring[next] = line
		next = (next + 1) % n
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return nil, err
	}
	return append(ring[next:len(ring):len(ring)], ring[:next]...), nil
}
