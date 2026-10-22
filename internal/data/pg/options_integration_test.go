//go:build integration

package pg

import (
	"crypto/tls"
	"errors"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// TestOtherRoles: an engine with tempoBase's roles and application name
// sets up its own roles next to Dock's, runs as them, and leaves Dock's
// logins working.
func TestOtherRoles(t *testing.T) {
	ctx := ctxT(t)
	tb := New(Options{Roles: data.TempoBaseRoles, AppName: "tempobase"})
	t.Cleanup(tb.Close)
	tg := data.Target{
		ServiceID: 80, Dial: dial, Database: "postgres",
		Reader:       data.Credentials{User: "tempobase_reader", Password: "tb-reader"},
		Writer:       data.Credentials{User: "tempobase_writer", Password: "tb-writer"},
		ReaderMasked: data.Credentials{User: "tempobase_reader_masked", Password: "tb-masked"},
	}
	t.Cleanup(func() {
		ownerExec(t, ctxT(t), "DROP OWNED BY tempobase_reader, tempobase_writer, tempobase_reader_masked; DROP ROLE tempobase_reader, tempobase_writer, tempobase_reader_masked")
	})
	if _, err := tb.Setup(ctx, tg, owner, tg.Reader, tg.Writer); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.SyncGrants(ctx, tg, owner, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Status(ctx, tg); err != nil {
		t.Fatal(err)
	}
	var frames []apitypes.DataQueryFrame
	_, err := tb.Query(ctx, tg, data.Call{Actor: "ada"}, apitypes.DataQueryRequest{SQL: "SELECT current_user, current_setting('application_name'), (SELECT count(*) > 0 FROM items)"},
		func(f apitypes.DataQueryFrame) error {
			if f.Type == apitypes.FrameRows {
				frames = append(frames, f)
			}
			return nil
		})
	if err != nil || len(frames) != 1 || string(frames[0].Rows[0]) != `["tempobase_reader","tempobase:ada",true]` {
		t.Fatalf("query as tempoBase: %+v %v", frames, err)
	}
	if _, err := newEngine(t).Status(ctx, target(1)); err != nil {
		t.Fatalf("Dock's reader after tempoBase's setup: %v", err)
	}
}

// TestCheck: Check reports the server for an owner that can set up, and
// says why not for one that can't, without changing anything.
func TestCheck(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	info, err := e.Check(ctx, target(0), owner)
	if err != nil || info.VersionNum < 170000 {
		t.Fatalf("superuser: %+v %v", info, err)
	}
	ownerExec(t, ctx, "CREATE ROLE plain LOGIN PASSWORD 'plain-pw'")
	t.Cleanup(func() { ownerExec(t, ctxT(t), "DROP ROLE plain") })
	if info, err := e.Check(ctx, target(0), data.Credentials{User: "plain", Password: "plain-pw"}); !errors.Is(err, data.ErrCannotSetUp) || info.VersionNum == 0 {
		t.Fatalf("plain owner: %+v %v", info, err)
	}
	if _, err := e.Check(ctx, target(0), data.Credentials{User: "postgres", Password: "wrong"}); !errors.Is(err, data.ErrLoginFailed) {
		t.Fatalf("wrong password: %v", err)
	}
}

// TestTLSModes: the test server has no TLS, so required TLS fails and
// optional TLS falls back to plaintext.
func TestTLSModes(t *testing.T) {
	ctx := ctxT(t)
	e := newEngine(t)
	tg := target(81)
	tg.TLS = &tls.Config{ServerName: "localhost", MinVersion: tls.VersionTLS12}
	if _, err := e.Status(ctx, tg); !errors.Is(err, data.ErrUnreachable) {
		t.Fatalf("required TLS against a plaintext server: %v", err)
	}
	tg.TLSOptional = true
	if _, err := e.Status(ctx, tg); err != nil {
		t.Fatalf("optional TLS: %v", err)
	}
}
