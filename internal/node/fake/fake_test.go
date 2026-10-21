package fake

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

func TestDial(t *testing.T) {
	ctx := context.Background()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	r := New()
	r.DialTo(ln.Addr().String())
	c, err := r.Start(ctx, node.ContainerSpec{Name: "db", Labels: node.Labels{Service: "1"}})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := r.Dial(ctx, node.DialRequest{ContainerID: c.ID, Port: 5432})
	if err != nil {
		t.Fatalf("dial running service: %v", err)
	}
	_ = conn.Close()

	if _, err := r.Dial(ctx, node.DialRequest{ContainerID: "nope", Port: 5432}); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("dial unknown container: got %v, want ErrNotFound", err)
	}
	if err := r.Stop(ctx, c.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Dial(ctx, node.DialRequest{ContainerID: c.ID, Port: 5432}); err == nil {
		t.Fatal("dial stopped container: want an error")
	}
}
