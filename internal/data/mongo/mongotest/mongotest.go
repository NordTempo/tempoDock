// Package mongotest starts a MongoDB server in a container for the
// MongoDB engine's integration tests (`go test -tags integration`).
package mongotest

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

// Image is the server the tests run against: the template's line.
const Image = "mongo:8.0"

// Root and Password are the server's root user, as the template makes it.
const (
	Root     = "root"
	Password = "owner-pw"
)

// Server is one running test server.
type Server struct {
	Addr string
	name string
}

// Dial connects to the server.
func (s *Server) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", s.Addr)
}

// Stop removes the server's container.
func (s *Server) Stop() { _ = exec.Command("docker", "rm", "-fv", s.name).Run() }

// Start starts a server and waits until its root user logs in.
func Start() (*Server, error) {
	name := fmt.Sprintf("dock-mongotest-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::27017",
		"-e", "MONGO_INITDB_ROOT_USERNAME="+Root, "-e", "MONGO_INITDB_ROOT_PASSWORD="+Password,
		Image, "mongod", "--wiredTigerCacheSizeGB", "0.25").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run: %w: %s", err, out)
	}
	s := &Server{name: name}
	port, err := exec.Command("docker", "port", name, "27017/tcp").Output()
	if err != nil {
		s.Stop()
		return nil, fmt.Errorf("docker port: %w", err)
	}
	s.Addr = strings.TrimSpace(strings.SplitN(string(port), "\n", 2)[0])
	deadline := time.Now().Add(2 * time.Minute)
	for {
		c, err := s.Connect(context.Background(), Root, Password)
		if err == nil {
			_ = c.Close()
			return s, nil
		}
		if time.Now().After(deadline) {
			s.Stop()
			return nil, fmt.Errorf("not ready: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Connect logs in as user.
func (s *Server) Connect(ctx context.Context, user, password string) (*wire.Conn, error) {
	nc, err := s.Dial(ctx)
	if err != nil {
		return nil, err
	}
	_ = nc.SetDeadline(time.Now().Add(10 * time.Second))
	c := wire.New(nc)
	if _, err := c.Handshake("dock-test", user, password, "admin"); err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}
