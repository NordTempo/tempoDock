package tempobase

import (
	"context"
	"crypto/rand"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// testPassword is long enough for every password check.
const testPassword = "correct horse battery"

// Tests hash passwords at the lowest cost: a sign-in takes ~1 ms, not 50.
func init() { bcryptCost = bcrypt.MinCost }

// harness is an App over a temp database with a settable clock.
type harness struct {
	*App
	t     testing.TB
	ctx   context.Context
	clock time.Time
}

func newHarness(t testing.TB) *harness {
	t.Helper()
	st, err := tbstore.Open(t.Context(), filepath.Join(t.TempDir(), "tempobase.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Listen: DefaultListen, DataDir: t.TempDir(), AuditRetentionDays: DefaultRetentionDays}
	h := &harness{App: New(cfg, st, box, slog.New(slog.DiscardHandler)), t: t, ctx: t.Context(), clock: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	h.now = func() time.Time { return h.clock }
	return h
}

// admin creates the first admin through setup and returns them.
func (h *harness) admin() Actor {
	h.t.Helper()
	code, ok, err := h.StartSetup(h.ctx)
	if err != nil || !ok {
		h.t.Fatalf("StartSetup: ok %v, err %v", ok, err)
	}
	_, _, act, err := h.Setup(h.ctx, tbtypes.SetupRequest{Code: code, Email: "admin@example.com", Name: "Admin", Password: testPassword}, "127.0.0.1")
	if err != nil {
		h.t.Fatal(err)
	}
	return act
}

// user creates a person with role and returns them as an actor.
func (h *harness) user(email string, role tbtypes.Role) Actor {
	h.t.Helper()
	u, _, err := h.CreateUser(h.ctx, System, email, "Someone", role, testPassword)
	if err != nil {
		h.t.Fatal(err)
	}
	return Actor{Kind: "user", Name: u.Email, UserID: u.ID, Email: u.Email, Role: u.Role}
}

// connection saves a connection as the system.
func (h *harness) connection(name string, production bool) tbtypes.Connection {
	h.t.Helper()
	c, err := h.CreateConnection(h.ctx, System, tbtypes.CreateConnectionRequest{
		Name: name, URL: "postgres://owner:s3cret-pw@db.example.com:5433/" + name + "?sslmode=require", Production: production,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

// token creates a token for act and authenticates it.
func (h *harness) token(act Actor, req tbtypes.CreateTokenRequest) Actor {
	h.t.Helper()
	created, err := h.CreateToken(h.ctx, act, req)
	if err != nil {
		h.t.Fatal(err)
	}
	tok, err := h.AuthenticateToken(h.ctx, created.Secret)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// wantCode fails unless err has code.
func wantCode(t *testing.T, err error, code apitypes.ErrorCode) {
	t.Helper()
	if got := codeOf(err); got != code {
		t.Fatalf("err = %v (code %q), want code %q", err, got, code)
	}
}
