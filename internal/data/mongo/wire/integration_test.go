//go:build integration

package wire_test

import (
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/data/mongo/mongotest"
	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

var server *mongotest.Server

func TestMain(m *testing.M) {
	var err error
	if server, err = mongotest.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	server.Stop()
	os.Exit(code)
}

// A login with the right password works and says the version; a wrong
// one is the server's AuthenticationFailed, not a broken connection.
func TestLogin(t *testing.T) {
	c, err := server.Connect(t.Context(), mongotest.Root, mongotest.Password)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	_, err = server.Connect(t.Context(), mongotest.Root, "wrong")
	var se *wire.ServerError
	if !errors.As(err, &se) || se.Code != wire.CodeAuthFailed {
		t.Fatalf("a wrong password: %v", err)
	}
}

// Every BSON type Dock writes reads back the same from the server.
func TestRoundTrip(t *testing.T) {
	c, err := server.Connect(t.Context(), mongotest.Root, mongotest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	dec, _ := wire.ParseDecimal128("249.00")
	doc := wire.D{
		{Key: "_id", Value: wire.ObjectID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}},
		{Key: "s", Value: "héllo"}, {Key: "i", Value: int32(-7)}, {Key: "l", Value: int64(math.MaxInt64)},
		{Key: "f", Value: 1.5}, {Key: "dec", Value: dec}, {Key: "t", Value: true}, {Key: "n", Value: nil},
		{Key: "at", Value: wire.DateTime(1764235442000)}, {Key: "bin", Value: wire.Binary{Subtype: 4, Data: []byte("0123456789abcdef")}},
		{Key: "re", Value: wire.Regex{Pattern: "^a", Options: "i"}}, {Key: "nest", Value: wire.D{{Key: "a", Value: wire.A{int32(1), "two", wire.D{}}}}},
		{Key: "min", Value: wire.MinKey{}}, {Key: "max", Value: wire.MaxKey{}},
	}
	coll := fmt.Sprintf("docs%d", time.Now().UnixNano())
	ins, err := c.Run("wiretest", wire.D{{Key: "insert", Value: coll}, {Key: "documents", Value: wire.A{doc}}})
	if n, _ := ins.Get("n"); err != nil || n != int32(1) {
		t.Fatalf("insert: %v, %v", wire.Compact(ins), err)
	}
	if werr, _ := ins.Get("writeErrors"); werr != nil {
		t.Fatalf("insert: %s", wire.Compact(werr))
	}
	reply, err := c.Run("wiretest", wire.D{{Key: "find", Value: coll}, {Key: "filter", Value: wire.D{}}, {Key: "singleBatch", Value: true}})
	if err != nil {
		t.Fatal(err)
	}
	cursor, _ := reply.Get("cursor")
	batch, _ := cursor.(wire.D).Get("firstBatch")
	docs := batch.(wire.A)
	if len(docs) != 1 || !wire.Equal(docs[0], doc) {
		t.Fatalf("read back:\n%s\nwrote:\n%s", wire.Compact(docs), wire.Compact(doc))
	}
	// A command error is the server's, and the connection goes on.
	_, err = c.Run("wiretest", wire.D{{Key: "nope", Value: int32(1)}})
	var se *wire.ServerError
	if !errors.As(err, &se) || se.Code != wire.CodeCommandAbsent || c.Broken() {
		t.Fatalf("an unknown command: %v (broken %v)", err, c.Broken())
	}
}

// A password with a non-breaking space logs in as the server prepared
// it (SASLprep), as an owner's own password may hold one.
func TestSASLPrepLogin(t *testing.T) {
	c, err := server.Connect(t.Context(), mongotest.Root, mongotest.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	pw := "nb\u00a0space"
	_, _ = c.Run("admin", wire.D{{Key: "dropUser", Value: "prep"}})
	if _, err := c.Run("admin", wire.D{{Key: "createUser", Value: "prep"}, {Key: "pwd", Value: pw}, {Key: "roles", Value: wire.A{}}, {Key: "mechanisms", Value: wire.A{"SCRAM-SHA-256"}}}); err != nil {
		t.Fatal(err)
	}
	u, err := server.Connect(t.Context(), "prep", pw)
	if err != nil {
		t.Fatalf("login with a password SASLprep changes: %v", err)
	}
	_ = u.Close()
}
