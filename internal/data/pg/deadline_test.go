package pg

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/NordTempo/tempoDock/internal/data"
)

// silentServer logs every connection in and then never answers.
func silentServer(t *testing.T) (dial func(context.Context) (net.Conn, error)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	serve := func(nc net.Conn) {
		defer func() { _ = nc.Close() }()
		be := pgproto3.NewBackend(nc, nc)
		if _, err := be.ReceiveStartupMessage(); err != nil {
			return
		}
		be.Send(&pgproto3.AuthenticationOk{})
		be.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.2"})
		be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
		be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		_ = be.Flush()
		_, _ = io.Copy(io.Discard, nc)
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(nc)
		}
	}()
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}
}

// A call whose context has no deadline still ends when the server stops
// answering, and gives its connection's place in the cache back: on a
// cached connection (Status, Schema) and on the owner's (Check).
func TestCallsEndWhenTheServerStopsAnswering(t *testing.T) {
	e := New(Options{})
	defer e.Close()
	e.callTimeout = 200 * time.Millisecond
	login := data.Credentials{User: "dock_reader", Password: "pw"}
	tg := data.Target{ServiceID: 1, Dial: silentServer(t), Database: "app", Reader: login}
	ctx := context.Background()
	calls := map[string]func() error{
		"status": func() error { _, err := e.Status(ctx, tg); return err },
		"schema": func() error { _, err := e.Schema(ctx, tg, data.Call{Actor: "dev@x"}, nil); return err },
		"check":  func() error { _, err := e.Check(ctx, tg, login); return err },
	}
	for name, call := range calls {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: no error from a server that never answered", name)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: still waiting on the server", name)
		}
		if n := e.conns.Len(); n != 0 {
			t.Errorf("%s: %d connection(s) still held", name, n)
		}
	}
}
