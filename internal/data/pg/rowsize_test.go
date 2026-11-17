package pg

import (
	"context"
	"encoding/binary"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// oneRowServer speaks just enough of the protocol for Engine.Query and
// answers the statement with one row whose single cell is cell bytes.
func oneRowServer(t *testing.T, cell int) (dial func(context.Context) (net.Conn, error)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan int, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			done <- 0
			return
		}
		defer func() { _ = nc.Close() }()
		be := pgproto3.NewBackend(nc, nc)
		if _, err := be.ReceiveStartupMessage(); err != nil {
			done <- 0
			return
		}
		be.Send(&pgproto3.AuthenticationOk{})
		be.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.2"})
		be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
		be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		_ = be.Flush()
		wrote := 0
		for {
			msg, err := be.Receive()
			if err != nil {
				done <- wrote
				return
			}
			switch msg.(type) {
			case *pgproto3.Query:
				be.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
				be.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
			case *pgproto3.Parse:
				be.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Describe:
				be.Send(&pgproto3.ParameterDescription{})
				be.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("v"), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1}}})
			case *pgproto3.Bind:
				be.Send(&pgproto3.BindComplete{})
			case *pgproto3.Execute:
				// One DataRow: 'D', length, one column of cell bytes.
				head := []byte{'D', 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}
				binary.BigEndian.PutUint32(head[1:], uint32(4+2+4+cell))
				binary.BigEndian.PutUint32(head[7:], uint32(cell))
				if _, err := nc.Write(head); err != nil {
					done <- wrote
					return
				}
				chunk := make([]byte, 1<<20)
				for i := range chunk {
					chunk[i] = 'x'
				}
				for wrote < cell {
					n, err := nc.Write(chunk[:min(len(chunk), cell-wrote)])
					wrote += n
					if err != nil {
						done <- wrote
						return
					}
				}
				be.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
			case *pgproto3.Sync:
				be.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
			case *pgproto3.Terminate:
				done <- wrote
				return
			}
			_ = be.Flush()
		}
	}()
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}
}

// A row is read whole before the panel's caps see it, so its size is
// bounded where it is read: a row past maxMessageBytes fails the
// statement with words that say what to do, and costs the daemon next to
// nothing; a large row under it is read and its cell cut as before.
func TestQueryRowSizeIsBounded(t *testing.T) {
	query := func(cell int) (frames []apitypes.DataQueryFrame, allocated int64) {
		e := New(Options{})
		defer e.Close()
		tg := data.Target{ServiceID: 1, Dial: oneRowServer(t, cell), Database: "app", Reader: data.Credentials{User: "dock_reader", Password: "pw"}}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if _, err := e.Query(ctx, tg, data.Call{Actor: "dev@x"}, apitypes.DataQueryRequest{SQL: "SELECT big FROM t"}, func(f apitypes.DataQueryFrame) error {
			frames = append(frames, f)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		return frames, int64(after.TotalAlloc - before.TotalAlloc)
	}

	frames, allocated := query(4 * maxMessageBytes)
	var failed *apitypes.DataQueryError
	rows := 0
	for _, f := range frames {
		rows += len(f.Rows)
		if f.Type == apitypes.FrameError {
			failed = f.Error
		}
	}
	if failed == nil || failed.Message != tooLargeText || rows != 0 {
		t.Fatalf("a row of %d MiB: %d rows, error %+v", 4*maxMessageBytes>>20, rows, failed)
	}
	if allocated > maxMessageBytes {
		t.Errorf("refusing a %d MiB row allocated %d MiB", 4*maxMessageBytes>>20, allocated>>20)
	}

	frames, _ = query(maxMessageBytes / 2)
	rows = 0
	for _, f := range frames {
		if f.Type == apitypes.FrameError {
			t.Fatalf("a row under the bound: %+v", f.Error)
		}
		for _, r := range f.Rows {
			rows++
			if len(r) > apitypes.DataCellMaxBytes+1024 {
				t.Errorf("the row's cell wasn't cut: %d bytes", len(r))
			}
		}
	}
	if rows != 1 {
		t.Fatalf("a row under the bound: %d rows", rows)
	}
}
