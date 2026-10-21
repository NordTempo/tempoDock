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

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/cli/apiclient"
	"github.com/NordTempo/platform/internal/cli/output"
)

// logsFlags are the options of `dock logs`.
type logsFlags struct {
	targetFlags
	follow, build bool
	tail          int
	since         string
	release       int64
}

func newLogsCmd(g *globals) *cobra.Command {
	var f logsFlags
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show a service's output, or a release's build log",
		Long: `Print the output of the service's active release (or --release N), read
from the server's container runtime. --build prints a build log instead
(default: the newest release's).

With --json, every line is one JSON object on its own line.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
	cmd.Flags().BoolVar(&f.build, "build", false, "show the build log instead of service output")
	return cmd
}

func (g *globals) serviceLogs(ctx context.Context, s *session, t target, f logsFlags) error {
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
