//go:build integration

package docker

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// TestSyncKeepsEveryCommittedWrite is a move's copy against a real
// database (ADR 0062): Postgres takes inserts the whole time the live
// passes copy its volume, then stops cleanly (the fence), the final pass
// copies the rest and a check pass finds nothing different. A Postgres
// started on the copy has every row whose insert was acknowledged.
func TestSyncKeepsEveryCommittedWrite(t *testing.T) {
	const pgImage = "postgres:17-alpine"
	e := newEnv(t)
	e.Sync = buildSync(t)
	ctx := ctxTimeout(t, 5*time.Minute)
	if ok, _ := e.HasImage(ctx, pgImage); !ok {
		if _, err := e.Pull(ctx, pgImage, nil); err != nil {
			t.Fatal(err)
		}
	}
	src, dst, acks := e.name("src"), e.name("dst"), e.name("acks")
	labels := node.Labels{Project: "p1"}
	for _, v := range []string{src, dst, acks} {
		if err := e.EnsureVolume(ctx, v, labels); err != nil {
			t.Fatal(err)
		}
	}
	// The writer: one insert after another, each number written to the
	// acks volume once its insert committed.
	db := e.start(node.ContainerSpec{
		Name: e.name("db"), Image: pgImage, Labels: labels,
		Env: map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust"},
		Mounts: []node.Mount{{Volume: src, Path: "/var/lib/postgresql/data"}, {Volume: acks, Path: "/acks"}},
		Cmd: `docker-entrypoint.sh postgres & pg=$!
trap 'kill -INT $pg; wait $pg; exit 0' TERM
until pg_isready -h 127.0.0.1 -U postgres -q; do sleep 0.2; done
psql -h 127.0.0.1 -U postgres -qc 'create table t (n int primary key)'
(i=0; while true; do i=$((i+1)); psql -h 127.0.0.1 -U postgres -qc "insert into t values ($i)" && echo $i >> /acks/log; done) &
wait $pg`,
	})
	shell := func(name, script string, mounts ...node.Mount) string {
		t.Helper()
		res, err := e.RunJob(ctx, node.JobSpec{Name: e.name(name), Image: shellImage, Labels: labels, Script: script, Mounts: mounts})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: %+v, %v", name, res, err)
		}
		return strings.TrimSpace(res.Output)
	}
	acked := func() int {
		n, _ := strconv.Atoi(shell("count", "touch /acks/log && wc -l < /acks/log", node.Mount{Volume: acks, Path: "/acks"}))
		return n
	}
	for deadline := time.Now().Add(time.Minute); acked() < 50; {
		if time.Now().After(deadline) {
			t.Fatal("the writer didn't start writing")
		}
		time.Sleep(500 * time.Millisecond)
	}

	for i := range 2 {
		before := acked()
		e.syncPass(ctx, t, src, dst, "--live")
		if acked() == before {
			t.Fatalf("live pass %d: nothing was written meanwhile, so the test proves nothing", i+1)
		}
	}
	if err := e.Stop(ctx, db.ID, 30*time.Second); err != nil { // the fence
		t.Fatal(err)
	}
	e.syncPass(ctx, t, src, dst, "")
	e.syncPass(ctx, t, src, dst, "--check")
	last := acked()

	res, err := e.RunJob(ctx, node.JobSpec{
		Name: e.name("verify"), Image: pgImage, Labels: labels, Mounts: []node.Mount{{Volume: dst, Path: "/var/lib/postgresql/data"}},
		Script: `docker-entrypoint.sh postgres >/dev/null 2>&1 &
until pg_isready -h 127.0.0.1 -U postgres -q; do sleep 0.2; done
psql -h 127.0.0.1 -U postgres -tAc 'select count(*) from t where n <= ` + strconv.Itoa(last) + `'`,
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Postgres on the copy: %+v, %v", res, err)
	}
	lines := strings.Fields(res.Output)
	if got := lines[len(lines)-1]; got != strconv.Itoa(last) {
		t.Fatalf("the copy has %s of the %d acknowledged rows", got, last)
	}
	t.Logf("%d acknowledged rows, all on the copy", last)
}

// syncPass runs one dock-sync pass from volume src to dst the way core
// does: recv on dst, send on src, each one's output the other's input.
func (e *testEnv) syncPass(ctx context.Context, t *testing.T, src, dst, mode string) {
	t.Helper()
	manR, manW := io.Pipe()
	opsR, opsW := io.Pipe()
	var recvErr, sendErr strings.Builder
	type result struct {
		res node.JobResult
		err error
	}
	recv := make(chan result, 1)
	go func() {
		res, err := e.RunJob(ctx, node.JobSpec{
			Name: e.name("recv-" + strconv.FormatInt(time.Now().UnixNano(), 36)), Image: shellImage, Labels: node.Labels{Project: "p1"},
			Script: "exec " + node.SyncPath + " recv /v", Sync: true, Mounts: []node.Mount{{Volume: dst, Path: "/v"}},
			Stream: &node.JobStream{Stdin: opsR, Stdout: manW, Stderr: &recvErr},
		})
		_ = manW.CloseWithError(err)
		_ = opsR.Close()
		recv <- result{res, err}
	}()
	res, err := e.RunJob(ctx, node.JobSpec{
		Name: e.name("send-" + strconv.FormatInt(time.Now().UnixNano(), 36)), Image: shellImage, Labels: node.Labels{Project: "p1"},
		Script: "exec " + node.SyncPath + " send " + mode + " /v", Sync: true, Mounts: []node.Mount{{Volume: src, Path: "/v", ReadOnly: true}},
		Stream: &node.JobStream{Stdin: manR, Stdout: opsW, Stderr: &sendErr},
	})
	_ = opsW.CloseWithError(err)
	_ = manR.Close()
	r := <-recv
	if err != nil || res.ExitCode != 0 || r.err != nil || r.res.ExitCode != 0 {
		t.Fatalf("pass %q: send %d %v: %s; recv %d %v: %s", mode, res.ExitCode, err, sendErr.String(), r.res.ExitCode, r.err, recvErr.String())
	}
	t.Logf("pass %q: %s", mode, strings.TrimSpace(sendErr.String()))
}

// buildSync builds cmd/dock-sync for this machine (static, like `make
// probe`) and returns its path.
func buildSync(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dock-sync")
	cmd := exec.Command("go", "build", "-trimpath", "-o", path, "github.com/NordTempo/tempoDock/cmd/dock-sync")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build dock-sync: %v\n%s", err, out)
	}
	return path
}
