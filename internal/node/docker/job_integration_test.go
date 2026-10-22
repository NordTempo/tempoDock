//go:build integration

package docker

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// A job reads its input on stdin to the end. When reading the input
// fails, the job is killed before it sees the end of it: a script that
// would take the cut-short input for the whole never finishes.
func TestRunJobStdin(t *testing.T) {
	e := newEnv(t)
	ctx := ctxTimeout(t, time.Minute)
	res, err := e.RunJob(ctx, node.JobSpec{
		Name: e.name("stdin"), Image: shellImage, Script: `wc -c; echo done`,
		Stdin: strings.NewReader(strings.Repeat("x", 1<<20)),
	})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "1048576") || !strings.Contains(res.Output, "done") {
		t.Fatalf("job = %+v, %v", res, err)
	}

	broken := io.MultiReader(strings.NewReader(strings.Repeat("y", 64<<10)), errReader{errors.New("disk on fire")})
	res, err = e.RunJob(ctx, node.JobSpec{
		Name: e.name("broken"), Image: shellImage, Script: `cat >/dev/null; echo finished`,
		Stdin: broken,
	})
	if err == nil || !strings.Contains(err.Error(), "disk on fire") || res.ExitCode == 0 || strings.Contains(res.Output, "finished") {
		t.Fatalf("broken input: %+v, %v", res, err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
