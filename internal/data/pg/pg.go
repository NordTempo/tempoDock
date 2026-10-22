// Package pg is the Postgres engine of the database panel (ADR 0016): a
// data.Engine on pgx v5.
//
// Connections are dialed through data.Target.Dial (never a TCP address),
// cached per (service, role), at most data.MaxConnsPerKey per key and
// data.MaxConns in total, and closed by a per-connection timer
// data.IdleTimeout after their last use. There is no pool goroutine: an idle
// engine holds nothing but the timers of connections still open.
//
// Every read runs as dock_reader in a READ ONLY transaction that is rolled
// back afterwards; commits run as dock_writer in one transaction. Results are
// requested in Postgres' text format and encoded to JSON cells straight from
// the wire bytes (apitypes.DataRow), without decoding into Go values.
//
// Query reuses its buffers between frames: send must be done with a frame
// (encode or copy it) before it returns.
package pg

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// Engine is the Postgres data.Engine. Create it with New; it is safe for
// concurrent use.
type Engine struct {
	mu      sync.Mutex
	conns   map[connKey][]*conn
	total   int
	waiters int
	// wake is closed (and replaced) when a connection is released or
	// removed while someone waits in acquire.
	wake   chan struct{}
	closed bool
	// schemas caches introspection per service for schemaTTL; a service's
	// entry is dropped with its last connection or by Forget.
	schemas map[int64]*catalog

	idleTimeout time.Duration
	schemaTTL   time.Duration
	maxPerKey   int
	maxConns    int
	// countTimeout bounds each exact count of a never-analysed table
	// (countUnanalysed).
	countTimeout time.Duration
}

var _ data.Engine = (*Engine)(nil)

// New returns an engine with the limits of package data. It opens nothing
// until the first call.
func New() *Engine {
	return &Engine{
		conns:       map[connKey][]*conn{},
		wake:        make(chan struct{}),
		schemas:     map[int64]*catalog{},
		idleTimeout: data.IdleTimeout,
		schemaTTL:   data.SchemaTTL,
		maxPerKey:   data.MaxConnsPerKey,
		maxConns:    data.MaxConns,

		countTimeout: exactCountTimeout,
	}
}

// Kind is Postgres.
func (e *Engine) Kind() apitypes.DataEngine { return apitypes.EnginePostgres }

// Status logs in as the reader, checks the server version, that the role
// still has pg_read_all_data and, with t.ReaderMasked set, that the masked
// reader exists (databases set up before it need setup again).
func (e *Engine) Status(ctx context.Context, t data.Target) (data.ServerInfo, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return data.ServerInfo{}, err
	}
	defer e.release(cn)
	var num int
	var reads, masked bool
	err = cn.pg.QueryRow(ctx, "SELECT current_setting('server_version_num')::int, pg_has_role(current_user, 'pg_read_all_data', 'USAGE'), to_regrole($1) IS NOT NULL",
		string(data.RoleReaderMasked)).Scan(&num, &reads, &masked)
	if err != nil {
		cn.broken = true
		return data.ServerInfo{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	info := serverInfo(cn.pg, num)
	if num < data.MinPostgresVersion {
		return info, unsupported(info)
	}
	if !reads {
		return info, fmt.Errorf("%w: %s can't read all data; run setup again", data.ErrNotSetUp, data.RoleReader)
	}
	if t.ReaderMasked.User != "" && !masked {
		return info, fmt.Errorf("%w: %s is missing; run setup again", data.ErrNotSetUp, data.RoleReaderMasked)
	}
	return info, nil
}

// serverInfo builds ServerInfo from server_version_num and the connection's
// server_version parameter ("17.2 (Debian ...)" becomes "17.2").
func serverInfo(pg *pgx.Conn, num int) data.ServerInfo {
	v := pg.PgConn().ParameterStatus("server_version")
	if i := strings.IndexByte(v, ' '); i > 0 {
		v = v[:i]
	}
	return data.ServerInfo{Version: v, VersionNum: num}
}

func unsupported(info data.ServerInfo) error {
	return fmt.Errorf("%w: Postgres %s is older than 14", data.ErrUnsupported, info.Version)
}

// Forget closes the service's idle connections (busy ones close when
// released) and drops its cached schema.
func (e *Engine) Forget(serviceID int64) {
	e.mu.Lock()
	idle := e.dropLocked(func(k connKey) bool { return k.service == serviceID })
	delete(e.schemas, serviceID)
	e.mu.Unlock()
	for _, c := range idle {
		c.close()
	}
}

// Close closes every connection; busy ones close when released, and later
// calls fail.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	idle := e.dropLocked(func(connKey) bool { return true })
	clear(e.schemas)
	e.broadcastLocked()
	e.mu.Unlock()
	for _, c := range idle {
		c.close()
	}
}
