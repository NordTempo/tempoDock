package tempobase

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// checkOwner logs in to a database as its owner without changing
// anything and reports the server and whether the owner can create
// tempoBase's roles (CREATEROLE or superuser).
//
// Pending ADR 0024 A.3: the engine gains Check(ctx, target, owner) with
// TLS on data.Target; this file is then deleted and TestConnection calls
// the engine, so the test and setup log in the same way.
func checkOwner(ctx context.Context, c datapanel.Conn) (info data.ServerInfo, canSetUp bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*dialTimeout)
	defer cancel()
	cfg, err := pgx.ParseConfig("host=tempobase port=5432 user=tempobase sslmode=disable")
	if err != nil {
		return info, false, err
	}
	cfg.User, cfg.Password, cfg.Database = c.Owner.User, c.Owner.Password, c.Database
	cfg.TLSConfig, cfg.Fallbacks, cfg.ValidateConnect, cfg.AfterConnect = c.TLS, nil, nil, nil
	if c.TLS != nil && c.TLSOptional {
		cfg.Fallbacks = []*pgconn.FallbackConfig{{Host: cfg.Host, Port: cfg.Port}}
	}
	cfg.ConnectTimeout = dialTimeout
	dial := c.Dial
	cfg.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.RuntimeParams = map[string]string{"application_name": "tempobase"}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return info, false, checkError(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	err = conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::int, current_setting('server_version'),
		coalesce((SELECT rolsuper OR rolcreaterole FROM pg_roles WHERE rolname = current_user), false)`).
		Scan(&info.VersionNum, &info.Version, &canSetUp)
	if err != nil {
		return info, false, checkError(err)
	}
	if f := strings.Fields(info.Version); len(f) > 0 {
		info.Version = f[0]
	}
	if info.VersionNum < data.MinPostgresVersion {
		return info, canSetUp, fmt.Errorf("%w: Postgres %s is older than 14", data.ErrUnsupported, info.Version)
	}
	return info, canSetUp, nil
}

// checkError says what failed in plain words, without the password.
func checkError(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		if strings.HasPrefix(pgErr.Code, "28") {
			return fmt.Errorf("%w: %s; check the user and password in the URL", data.ErrLoginFailed, pgErr.Message)
		}
		return fmt.Errorf("%w: %s", data.ErrUnreachable, pgErr.Message)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: no answer within %s", data.ErrUnreachable, 2*dialTimeout)
	}
	if ce, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		err = ce.Unwrap()
	}
	return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
}
