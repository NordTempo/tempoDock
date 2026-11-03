package tempobase

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// Values sealed before ADR 0041 are bound once, and a renamed connection
// still reaches its database.
func TestResealSecrets(t *testing.T) {
	ctx := t.Context()
	key := bytes.Repeat([]byte{7}, secrets.KeySize)
	st, err := tbstore.Open(ctx, filepath.Join(t.TempDir(), "tempobase.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _ := secrets.New(key)
	a := New(Config{DataDir: t.TempDir()}, st, box, slog.New(slog.DiscardHandler))

	// As tempoBase stored them before: unbound.
	c, err := st.CreateConnection(ctx, tbstore.CreateConnectionParams{
		Name: "orders", Engine: "postgres", Host: "db.example.com", Port: 5432, Database: "orders", Username: "owner",
		TlsMode: "disable", UrlSecret: sealUnbound(t, key, "postgres://owner:pw@db.example.com/orders?sslmode=disable"),
		CreatedAt: time.Now(), CreatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDataAccess(ctx, tbstore.UpsertDataAccessParams{
		ConnectionID: c.ID, Engine: "postgres", ReaderSecret: sealUnbound(t, key, "r"), WriterSecret: sealUnbound(t, key, "w"),
		SetUpAt: time.Now(), SetUpBy: "admin",
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.resealSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ = st.GetConnectionByName(ctx, "orders")
	acc, _ := st.GetDataAccess(ctx, c.ID)
	id := strconv.FormatInt(c.ID, 10)
	for _, v := range []struct {
		sealed []byte
		aad    secrets.AAD
		want   string
	}{
		{c.UrlSecret, sealedConnURL.AAD("orders"), "postgres://owner:pw@db.example.com/orders?sslmode=disable"},
		{acc.ReaderSecret, sealedDataReader.AAD(id), "r"},
		{acc.WriterSecret, sealedDataWriter.AAD(id), "w"},
	} {
		if got, err := box.Open(v.sealed, v.aad); v.sealed[0] != 2 || err != nil || string(got) != v.want {
			t.Errorf("format %d, open %s = %q, %v", v.sealed[0], v.aad, got, err)
		}
	}
	if _, err := box.Open(acc.WriterSecret, sealedDataReader.AAD(id)); err == nil {
		t.Error("the writer's password opened as the reader's")
	}

	// A rename seals the URL for the new name.
	if _, err := a.UpdateConnection(ctx, System, "orders", tbtypes.UpdateConnectionRequest{Name: new("orders-v2")}); err != nil {
		t.Fatal(err)
	}
	c, _ = st.GetConnectionByName(ctx, "orders-v2")
	if conn, err := a.Host().Connect(ctx, &c); err != nil || conn.Owner.Password != "pw" {
		t.Fatalf("Connect after a rename = %+v, %v", conn, err)
	}
}

// sealUnbound seals as tempoBase did before ADR 0041.
func sealUnbound(t *testing.T, key []byte, plaintext string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, _ := cipher.NewGCM(block)
	out := make([]byte, 1+gcm.NonceSize())
	out[0] = 1
	_, _ = rand.Read(out[1:])
	return gcm.Seal(out, out[1:], []byte(plaintext), nil)
}
