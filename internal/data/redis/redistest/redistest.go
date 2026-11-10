// Package redistest starts Redis and Valkey servers in containers for the
// Redis engine's integration tests (`go test -tags integration`).
package redistest

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/data/redis/resp"
)

// Password is the default user's (requirepass) on every test server.
const Password = "owner-pw"

// Images are the servers the tests run against.
var Images = []string{"redis:8", "valkey/valkey:8"}

// Server is one running test server.
type Server struct {
	Image string
	Addr  string
	name  string
}

// Dial connects to the server.
func (s *Server) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", s.Addr)
}

// Stop removes the server's container.
func (s *Server) Stop() { _ = exec.Command("docker", "rm", "-fv", s.name).Run() }

// StartAll starts one server per image at once and waits until each
// answers.
func StartAll(images ...string) ([]*Server, error) {
	servers := make([]*Server, len(images))
	errs := make([]error, len(images))
	var wg sync.WaitGroup
	for i, image := range images {
		wg.Go(func() { servers[i], errs[i] = start(image) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			for _, s := range servers {
				if s != nil {
					s.Stop()
				}
			}
			return nil, fmt.Errorf("%s: %w", images[i], err)
		}
	}
	return servers, nil
}

func start(image string) (*Server, error) {
	name := fmt.Sprintf("dock-redistest-%d", time.Now().UnixNano())
	server := "redis-server"
	if strings.HasPrefix(image, "valkey") {
		server = "valkey-server"
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::6379", image,
		server, "--requirepass", Password).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run: %w: %s", err, out)
	}
	s := &Server{Image: image, name: name}
	port, err := exec.Command("docker", "port", name, "6379/tcp").Output()
	if err != nil {
		s.Stop()
		return nil, fmt.Errorf("docker port: %w", err)
	}
	s.Addr = strings.TrimSpace(strings.SplitN(string(port), "\n", 2)[0])
	deadline := time.Now().Add(time.Minute)
	for {
		if err := s.ping(); err == nil {
			return s, nil
		} else if time.Now().After(deadline) {
			s.Stop()
			return nil, fmt.Errorf("not ready: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *Server) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	nc, err := s.Dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = nc.Close() }()
	_ = nc.SetDeadline(time.Now().Add(2 * time.Second))
	v, err := resp.New(nc).Do("AUTH", Password)
	if err != nil {
		return err
	}
	return v.Err()
}
