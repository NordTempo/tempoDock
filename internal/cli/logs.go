package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// logsFlags are the options of `dock logs`.
type logsFlags struct {
	targetFlags
	follow, build, stderr bool
	tail                  int
	since, grep           string
	release               int64
}

func newLogsCmd(g *globals) *cobra.Command {
	var f logsFlags
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show a service's output, or a release's build log",
		Long: `Print the output of the service's active release (or --release N), read
from the server's container runtime. An earlier release's container is
gone once it was replaced; its output was kept on its server when it was
removed, for [logs] keep_days (30) and up to keep_mb_per_service (64 MB)
per service, and --release N reads it (--follow ends after it). Without
--release, --follow goes on to the next release when a deploy replaces
the active one, and says why it ends otherwise. --build prints a build
log instead (default: the newest release's).

--grep TEXT keeps the lines containing TEXT (plain text; all lower case
matches any case) and --stderr the lines written to stderr; the server
filters, after --tail:
--tail 100 --grep x prints the matches among the last 100 lines.

With --json, every line is one JSON object on its own line.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.build && (f.grep != "" || f.stderr) {
				return &output.Error{Exit: output.ExitUsage, Message: "--grep and --stderr filter service output, not build logs",
					Hint: "Pipe the build log through grep: `dock logs --build | grep TEXT`."}
			}
			s, t, err := g.open(f.targetFlags, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if f.build {
				err = g.buildLog(ctx, s, t, f)
			} else {
				err = g.serviceLogs(ctx, s, t, f)
			}
			if errors.Is(err, context.Canceled) {
				return nil // Ctrl-C ends --follow
			}
			return err
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&f.follow, "follow", "f", false, "keep printing new lines")
	cmd.Flags().IntVarP(&f.tail, "tail", "n", 100, "lines of backlog to show, 0 for all")
	cmd.Flags().StringVar(&f.since, "since", "", "only lines newer than this: a duration like 10m or an RFC 3339 time")
	cmd.Flags().Int64VarP(&f.release, "release", "r", 0, "release number (default: the active release; with --build, the newest)")
	cmd.Flags().StringVar(&f.grep, "grep", "", "only lines containing this text (plain; lower case matches any case; after --tail)")
	cmd.Flags().BoolVar(&f.stderr, "stderr", false, "only lines written to stderr (after --tail)")
	cmd.Flags().BoolVar(&f.build, "build", false, "show the build log instead of service output")
	cmd.AddCommand(newLogDrainCmd(g))
	return cmd
}

// serviceLogs streams a release container's output, filtered on the
// server. Following the active release follows on to the next one when
// a deploy replaces it, as the Logs tab does, and says why it ended
// otherwise.
func (g *globals) serviceLogs(ctx context.Context, s *session, t target, f logsFlags) error {
	if !f.follow || f.release != 0 {
		return g.releaseLogs(ctx, s, t, f)
	}
	svc, err := get[apitypes.Service](ctx, s, t.servicePath())
	if err != nil {
		return err
	}
	if svc.ActiveRelease == nil {
		return g.releaseLogs(ctx, s, t, f) // the server says why there is nothing
	}
	for f.release = *svc.ActiveRelease; ; {
		if err := g.releaseLogs(ctx, s, t, f); err != nil {
			return err
		}
		next, why, err := g.nextLogs(ctx, s, t, f.release)
		if err != nil {
			return err
		}
		if next == 0 {
			g.io.Progressf("%s", why)
			return nil
		}
		g.io.Progressf("release %d of %s is live; following it", next, t)
		f.release = next
	}
}

// nextLogs is the release whose output a follow of release n goes on
// to once n's container ended: the newest release, if it is newer and
// live or deploying (a database's next release starts after the old one
// stops). Without one, why says why the follow ends.
func (g *globals) nextLogs(ctx context.Context, s *session, t target, n int64) (next int64, why string, err error) {
	rels, err := get[[]apitypes.Release](ctx, s, t.servicePath()+"/releases?limit=1")
	if err != nil {
		return 0, "", err
	}
	if len(rels) > 0 && rels[0].Number > n && (rels[0].Status == apitypes.ReleaseActive || !rels[0].Status.Done()) {
		return rels[0].Number, "", nil
	}
	svc, err := get[apitypes.Service](ctx, s, t.servicePath())
	if err != nil {
		return 0, "", err
	}
	if svc.StoppedAt != nil {
		return 0, fmt.Sprintf("%s was stopped; `dock service start` starts it", t), nil
	}
	return 0, fmt.Sprintf("the container of release %d stopped; `dock status` says why, and `dock logs -f` follows it again once it runs", n), nil
}

// releaseLogs streams one release's output (the active one without
// --release), filtered on the server.
func (g *globals) releaseLogs(ctx context.Context, s *session, t target, f logsFlags) error {
	q := url.Values{"tail": {strconv.Itoa(f.tail)}}
	if f.follow {
		q.Set("follow", "true")
	}
	if f.since != "" {
		q.Set("since", f.since)
	}
	if f.release != 0 {
		q.Set("release", itoa(f.release))
	}
	if f.grep != "" {
		q.Set("grep", f.grep)
	}
	if f.stderr {
		q.Set("stderr", "true")
	}
	return apiclient.Follow(ctx, s.api, t.servicePath()+"/logs?"+q.Encode(), func(l apitypes.LogLine) error {
		if g.io.JSON {
			return jsonLine(g.io.Out, l)
		}
		_, err := fmt.Fprintln(g.io.Out, l.Text)
		return err
	})
}

func (g *globals) buildLog(ctx context.Context, s *session, t target, f logsFlags) error {
	n := f.release
	if n == 0 {
		rels, err := get[[]apitypes.Release](ctx, s, t.servicePath()+"/releases?limit=1")
		if err != nil {
			return err
		}
		if len(rels) == 0 {
			return &output.Error{Exit: output.ExitNotFound, Message: t.String() + " has no releases yet", Hint: "Deploy with `dock deploy`."}
		}
		n = rels[0].Number
	}
	path := t.servicePath() + "/releases/" + itoa(n) + "/build-log"
	printLine := func(l apitypes.BuildLogLine) error {
		if g.io.JSON {
			return jsonLine(g.io.Out, l)
		}
		_, err := fmt.Fprintln(g.io.Out, l.Text)
		return err
	}
	if f.follow {
		return apiclient.Follow(ctx, s.api, path+"/stream", printLine)
	}
	req, err := s.api.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := s.api.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if !g.io.JSON {
		_, err = io.Copy(g.io.Out, resp.Body)
		return err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for seq := 0; sc.Scan(); seq++ {
		if err := printLine(apitypes.BuildLogLine{ReleaseNumber: n, Seq: seq, Text: sc.Text()}); err != nil {
			return err
		}
	}
	return sc.Err()
}
