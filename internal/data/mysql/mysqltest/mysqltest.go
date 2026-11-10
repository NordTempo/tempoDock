// Package mysqltest starts MySQL and MariaDB servers in containers for the
// MySQL engine's integration tests (`go test -tags integration`).
package mysqltest

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/data/mysql/wire"
)

// RootPassword is root's password on every test server.
const RootPassword = "owner-pw"

// Database is the database every test server creates.
const Database = "app"

// Images are the servers the tests run against: the oldest and newest
// lines Dock supports are checked by hand; CI runs the current LTS
// releases.
var Images = []string{"mysql:8.4", "mariadb:11.4"}

// Server is one running test server.
type Server struct {
	// Image is what it runs, e.g. "mysql:8.4".
	Image string
	// Addr is its port on the loopback interface.
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

// StartAll starts one server per image at once and waits until each
// accepts root's login. On an error it stops the ones that started.
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

// start runs image with root's password and the app database on a random
// loopback port, and waits up to two minutes for a login (the images
// initialise on a server without networking first).
func start(image string) (*Server, error) {
	name := fmt.Sprintf("dock-mysqltest-%d", time.Now().UnixNano())
	args := []string{"run", "-d", "--rm", "--name", name,
		"-e", "MYSQL_ROOT_PASSWORD=" + RootPassword, "-e", "MARIADB_ROOT_PASSWORD=" + RootPassword,
		"-e", "MYSQL_DATABASE=" + Database, "-e", "MARIADB_DATABASE=" + Database,
		"-p", "127.0.0.1::3306", image}
	if strings.HasPrefix(image, "mariadb") {
		// MariaDB ships performance_schema off; the insights tests need it.
		args = append(args, "--performance-schema=ON")
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run: %w: %s", err, out)
	}
	s := &Server{Image: image, name: name}
	out, err := exec.Command("docker", "port", name, "3306/tcp").Output()
	if err != nil {
		s.Stop()
		return nil, fmt.Errorf("docker port: %w", err)
	}
	s.Addr = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := s.ping()
		if err == nil {
			return s, nil
		}
		if time.Now().After(deadline) {
			s.Stop()
			return nil, fmt.Errorf("not ready: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ping logs in as root once.
func (s *Server) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	nc, err := s.Dial(ctx)
	if err != nil {
		return err
	}
	_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
	c, err := wire.Connect(nc, wire.Config{User: "root", Password: RootPassword, Database: Database})
	if err != nil {
		return err
	}
	return c.Close()
}
