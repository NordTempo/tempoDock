// Package tempobase is the standalone tempoBase server's domain (ADR
// 0024): people and how they sign in, API tokens, saved connections, the
// data access policy over them, the audit log, and the datapanel.Host
// that keeps the data panel's state. tbhttp serves it over HTTP; tbstore
// keeps it in SQLite.
//
// Every operation takes the calling Actor and checks what it may do
// itself, like Dock's core. The package must not import Dock's core, api,
// store, node, cli or mcp packages: the tempobase binary stays small.
package tempobase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/pg"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
)

// App is a running tempoBase: its store, key, data panel and in-memory
// state. Create it with Open; it is safe for concurrent use and does
// nothing on its own: the only timers are the engine's, each closing a
// database connection 60 s after its last use.
type App struct {
	cfg   Config
	store *tbstore.Store
	box   *secrets.Box
	log   *slog.Logger
	now   func() time.Time

	// engine talks to the saved databases as tempoBase's roles; panel is
	// the shared data panel over it with the App as its host.
	engine data.Engine
	panel  *datapanel.Panel[*tbstore.Connection]

	// bcrypt caps concurrent password hashes (each is ~50 ms of CPU).
	bcrypt chan struct{}
	// loginLimit rate limits sign-in, setup and password attempts per
	// client IP and email (httpkit.LoginLimiter).
	loginLimit *httpkit.LoginLimiter

	// setupMu guards setupCode, the one-time code that creates the first
	// admin; empty once used, or when there are users.
	setupMu   sync.Mutex
	setupCode string

	// pruneMu guards lastPrune, when the audit retention pass last ran.
	pruneMu   sync.Mutex
	lastPrune time.Time
}

// Open opens the data directory: the key file (created on first start)
// and the database (migrated, its sealed values bound: resealSecrets).
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	box, err := secrets.LoadOrCreate(cfg.SecretsKeyFile)
	if err != nil {
		return nil, err
	}
	st, err := tbstore.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, err
	}
	a := New(cfg, st, box, log)
	if err := a.resealSecrets(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("bind sealed values to where they are stored (ADR 0041): %w", err)
	}
	return a, nil
}

// New returns an App over an open store and key; Open is the usual way.
func New(cfg Config, st *tbstore.Store, box *secrets.Box, log *slog.Logger) *App {
	if log == nil {
		log = slog.Default()
	}
	a := &App{
		cfg: cfg, store: st, box: box, log: log, now: time.Now,
		engine:     pg.New(pg.Options{Roles: data.TempoBaseRoles, AppName: "tempobase"}),
		bcrypt:     make(chan struct{}, 2),
		loginLimit: httpkit.NewLoginLimiter(),
	}
	a.panel = datapanel.New(datapanel.Options[*tbstore.Connection]{
		Host: host{a}, Engine: a.engine, Box: box, Roles: data.TempoBaseRoles, Words: Words, Logger: log,
	})
	return a
}

// Words are tempoBase's wording in the data panel's messages and API docs.
var Words = datapanel.Words{
	Product:         "tempoBase",
	SetupHint:       "an admin can run Set up data access in the connection's settings",
	LoginFailedHint: "Check the user and password in the connection's URL (connection settings)",
	SetupAction:     "Set up data access in the connection's settings",
	ConfirmWhat:     "the connection's name",
	Database:        "connection",
}

// Close closes the database connections and the store.
func (a *App) Close() error {
	a.panel.Close()
	return a.store.Close()
}

// Store is the App's database, for `tempobase admin backup`.
func (a *App) Store() *tbstore.Store { return a.store }

// Config is the App's configuration.
func (a *App) Config() Config { return a.cfg }

// Errors callers see. They are datapanel.Error, the one domain error of
// both hosts (ADR 0024), so the API maps them by code.
var errf = datapanel.Errorf

// codeOf is err's code: "" for nil, CodeInternal for unexpected errors.
func codeOf(err error) apitypes.ErrorCode {
	if err == nil {
		return ""
	}
	return datapanel.CodeOf(err)
}

func invalid(format string, args ...any) error {
	return errf(apitypes.CodeValidation, format, args...)
}

func notFound(format string, args ...any) error {
	return errf(apitypes.CodeNotFound, format, args...)
}

func forbidden(format string, args ...any) error {
	return errf(apitypes.CodeForbidden, format, args...)
}

func conflict(format string, args ...any) error {
	return errf(apitypes.CodeConflict, format, args...)
}

// randomCode returns n random bytes, URL-safe base64 encoded.
func randomCode(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func ptr[T any](v T) *T { return &v }
