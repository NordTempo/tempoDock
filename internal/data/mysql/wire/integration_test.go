//go:build integration

package wire_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/data/mysql/mysqltest"
	"github.com/NordTempo/tempoDock/internal/data/mysql/wire"
)

var servers []*mysqltest.Server

func TestMain(m *testing.M) {
	var err error
	if servers, err = mysqltest.StartAll(mysqltest.Images...); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	for _, s := range servers {
		s.Stop()
	}
	os.Exit(code)
}

// connect logs in to s as user with cfg's other settings.
func connect(t *testing.T, s *mysqltest.Server, cfg wire.Config) (*wire.Conn, error) {
	t.Helper()
	nc, err := s.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = nc.SetDeadline(time.Now().Add(10 * time.Second))
	return wire.Connect(nc, cfg)
}

var root = wire.Config{User: "root", Password: mysqltest.RootPassword, Database: mysqltest.Database}

// TestLoginsAndQueries logs in with each plugin (fresh caching_sha2 users
// take the RSA full-auth path, then the fast path), over TLS where the
// server offers it, and reads typed results, NULLs, errors and OK
// packets.
func TestLoginsAndQueries(t *testing.T) {
	for _, s := range servers {
		t.Run(s.Image, func(t *testing.T) {
			c, err := connect(t, s, root)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			mysql := !strings.Contains(strings.ToLower(c.Version), "mariadb")

			// A user per plugin the server has.
			plugins := []string{"mysql_native_password"}
			if mysql {
				plugins = []string{"caching_sha2_password"}
			}
			for _, p := range plugins {
				for _, sql := range []string{
					"DROP USER IF EXISTS 'u_" + p + "'@'%'",
					// The server's default plugin: caching_sha2 on MySQL, native
					// on MariaDB.
					"CREATE USER 'u_" + p + "'@'%' IDENTIFIED BY 'pw''1'",
					"GRANT SELECT ON app.* TO 'u_" + p + "'@'%'",
				} {
					if _, err := c.Exec(sql); err != nil {
						t.Fatalf("%s: %v", sql, err)
					}
				}
				for range 2 {
					u, err := connect(t, s, wire.Config{User: "u_" + p, Password: "pw'1", Database: "app",
						Attributes: map[string]string{"program_name": "dock"}})
					if err != nil {
						t.Fatalf("login with %s: %v", p, err)
					}
					_ = u.Close()
				}
				if _, err := connect(t, s, wire.Config{User: "u_" + p, Password: "wrong"}); wire.ErrorCode(err) != 1045 {
					t.Fatalf("wrong password with %s: %v", p, err)
				}
			}
			// A user that doesn't exist: MySQL switches the login to
			// sha256_password before refusing it; it must still read as
			// access denied.
			if _, err := connect(t, s, wire.Config{User: "nobody", Password: "x"}); wire.ErrorCode(err) != 1045 {
				t.Fatalf("unknown user: %v", err)
			}
			tc, err := connect(t, s, wire.Config{User: "root", Password: mysqltest.RootPassword,
				TLS: &tls.Config{InsecureSkipVerify: true}, TLSOptional: true})
			if err != nil {
				t.Fatalf("TLS login: %v", err)
			}
			_ = tc.Close()

			var cols []wire.Column
			var rows [][]string
			err = c.Query("SELECT 1 AS a, NULL AS b, 'x''y' AS c, CAST(12.50 AS DECIMAL(5,2)) AS d, X'00ff' AS e, 9007199254740993 AS f",
				func(c []wire.Column) error { cols = c; return nil },
				func(v [][]byte) error {
					row := make([]string, len(v))
					for i, b := range v {
						if b == nil {
							row[i] = "<null>"
						} else {
							row[i] = string(b)
						}
					}
					rows = append(rows, row)
					return nil
				}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(cols) != 6 || cols[0].Name != "a" || cols[3].Type != wire.TypeNewDecimal || cols[4].Charset != wire.CharsetBinary || cols[5].Type != wire.TypeLongLong {
				t.Fatalf("columns %+v", cols)
			}
			if len(rows) != 1 || strings.Join(rows[0], "|") != "1|<null>|x'y|12.50|\x00\xff|9007199254740993" {
				t.Fatalf("rows %q", rows)
			}

			res, err := c.Exec("DO 1")
			if err != nil || res.AffectedRows != 0 {
				t.Fatalf("DO: %+v %v", res, err)
			}
			if _, err := c.Exec("SELECT * FROM nope"); wire.ErrorCode(err) != 1146 || c.Broken() {
				t.Fatalf("missing table: %v, broken %t", err, c.Broken())
			}
			// One statement per query: the server refuses two.
			if _, err := c.Exec("SELECT 1; SELECT 2"); wire.ErrorCode(err) != 1064 {
				t.Fatalf("two statements: %v", err)
			}
			if err := c.Ping(); err != nil {
				t.Fatal(err)
			}

			// A callback error abandons the result and breaks the connection.
			stop := errors.New("enough")
			err = c.Query("SELECT 1 UNION ALL SELECT 2", func([]wire.Column) error { return nil }, func([][]byte) error { return stop }, nil)
			if !errors.Is(err, stop) || !c.Broken() {
				t.Fatalf("abandoned: %v, broken %t", err, c.Broken())
			}
		})
	}
}

// TestKillQuery cancels a running query from a second connection, the way
// the engine cancels on a done context.
func TestKillQuery(t *testing.T) {
	for _, s := range servers {
		t.Run(s.Image, func(t *testing.T) {
			c, err := connect(t, s, root)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			k, err := connect(t, s, root)
			if err != nil {
				t.Fatal(err)
			}
			defer k.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { _, _ = k.Exec(fmt.Sprintf("KILL QUERY %d", c.ID)) })
			defer stop()
			start := time.Now()
			err = c.Query("SELECT SLEEP(10)", func([]wire.Column) error { return nil }, func([][]byte) error { return nil }, nil)
			// MySQL ends SLEEP with a 1 row result when killed; MariaDB
			// with error 1317 (query execution was interrupted).
			if (err != nil && wire.ErrorCode(err) != 1317) || time.Since(start) > 5*time.Second {
				t.Fatalf("killed sleep: %v after %s", err, time.Since(start))
			}
			if err := c.Ping(); err != nil {
				t.Fatalf("after kill: %v", err)
			}
		})
	}
}
