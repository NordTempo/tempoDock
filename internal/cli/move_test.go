package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// TestFollowMove: following a move prints each step and each service's
// status change once, ends with the move as it ended, and a failed move
// is an error that says it was rolled back.
func TestFollowMove(t *testing.T) {
	m := apitypes.EnvironmentMove{
		ID: 7, Project: "shop", Environment: "staging", From: "cp", To: "lab",
		Status: apitypes.MoveRunning, Step: apitypes.MovePrepare,
		Services: []apitypes.MoveService{{Service: "db", Stateful: true, Status: "waiting"}, {Service: "web", Status: "waiting"}},
	}
	at := func(step apitypes.MoveStep, db, web string, copied int64) apitypes.Event {
		n := m
		n.Step = step
		n.Services = []apitypes.MoveService{{Service: "db", Stateful: true, Status: db, CopiedBytes: copied}, {Service: "web", Status: web}}
		return apitypes.Event{Type: apitypes.EventEnvironmentMove, Move: &n}
	}
	other := m
	other.ID = 6
	failed := at(apitypes.MoveStart, "failed", "waiting", 812<<20)
	failed.Move.Status, failed.Move.Error = apitypes.MoveRollingBack, "db: health check failed"
	failed.Move.Services[0].Error = "health check failed"
	ended := *failed.Move
	ended.Status = apitypes.MoveFailed

	events := make(chan apitypes.Event, 16)
	for _, ev := range []apitypes.Event{
		at(apitypes.MoveStop, "stopped", "waiting", 0),
		{Type: apitypes.EventEnvironmentMove, Move: &other},
		at(apitypes.MoveCopy, "copied", "waiting", 812<<20),
		at(apitypes.MoveCopy, "copied", "waiting", 812<<20),
		failed,
		{Type: apitypes.EventEnvironmentMove, Move: &ended},
	} {
		events <- ev
	}
	var errOut bytes.Buffer
	g := &globals{io: &output.IO{Out: &bytes.Buffer{}, Err: &errOut}}
	w := &watcher{g: g, events: events, cancel: func() {}}

	got, err := w.followMove(context.Background(), m)
	if err != nil || got.Status != apitypes.MoveFailed {
		t.Fatalf("followMove: %v %+v", err, got)
	}
	out := errOut.String()
	for _, want := range []string{"stop: stateful services stop on cp", "stop: db stopped", "copy: db 812.0 MB", "start: db failed: health check failed", "Rolling back: db: health check failed"} {
		if strings.Count(out, want) != 1 {
			t.Errorf("want %q once in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "web") {
		t.Errorf("a waiting service was printed:\n%s", out)
	}
	if e, ok := errors.AsType[*output.Error](moveFailure(got)); !ok || !strings.Contains(e.Message, "db: health check failed") || !strings.Contains(e.Hint, "rolled back: shop/staging is still on cp") {
		t.Fatalf("moveFailure: %+v", e)
	}
}

// TestEnvironmentMoveCommand: --status before any move is not found, and
// a move is confirmed first and refused by the server with its reason.
func TestEnvironmentMoveCommand(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	servers := ok[[]apitypes.Server](t, "server", "list")
	if len(servers) != 1 {
		t.Fatalf("servers: %+v", servers)
	}

	if code, _, stderr := exec(t, "env", "move", "production", "-p", "shop", "--status"); code != int(output.ExitNotFound) || !strings.Contains(stderr, "never moved") {
		t.Fatalf("status: exit %d %s", code, stderr)
	}
	if code, _, stderr := exec(t, "env", "move", "production", "-p", "shop", "--to", servers[0].Name); code != int(output.ExitUsage) || !strings.Contains(stderr, "--yes") {
		t.Fatalf("unconfirmed: exit %d %s", code, stderr)
	}
	if code, _, stderr := exec(t, "env", "move", "production", "-p", "shop", "--to", servers[0].Name, "--yes"); code == 0 || !strings.Contains(stderr, "already") {
		t.Fatalf("same server: exit %d %s", code, stderr)
	}
}
