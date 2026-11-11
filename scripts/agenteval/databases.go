package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// database is a data case's own database container, which its daemon
// dials (DOCK_FAKE_DIAL). Each case gets one: `dock db setup` creates
// server-wide roles and rotates their passwords, so cases can't share.
type database struct {
	kind, name, addr string
}

// Postgres's owner password, which data cases give their service as
// POSTGRES_PASSWORD so Dock's setup can connect as the owner.
const pgPassword = "owner-pw"

// dbImages are the containers' run arguments and ports by kind.
var dbImages = map[string]struct {
	args []string
	port string
}{
	// pg_stat_statements loaded, for the Health view's slow queries.
	"postgres": {[]string{"-e", "POSTGRES_PASSWORD=" + pgPassword, "-e", "POSTGRES_DB=app", "postgres:17-alpine", "-c", "shared_preload_libraries=pg_stat_statements"}, "5432"},
	"redis":    {[]string{"redis:7-alpine"}, "6379"},
}

var dbSeq atomic.Int64

// startDatabase runs a database of kind and waits until it takes
// connections.
func startDatabase(kind string) (*database, error) {
	img, ok := dbImages[kind]
	if !ok {
		return nil, fmt.Errorf("unknown database %q", kind)
	}
	d := &database{kind: kind, name: fmt.Sprintf("dock-agenteval-%d-%d-%s", os.Getpid(), dbSeq.Add(1), kind)}
	args := append([]string{"run", "-d", "--rm", "--name", d.name, "-p", "127.0.0.1::" + img.port}, img.args...)
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run: %w: %s", err, out)
	}
	out, err := exec.Command("docker", "port", d.name, img.port+"/tcp").Output()
	if err != nil {
		d.stop()
		return nil, fmt.Errorf("docker port: %w", err)
	}
	d.addr = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	ready := []string{"exec", d.name, "redis-cli", "ping"}
	if kind == "postgres" {
		// The image's init runs a server without TCP first; wait for the real one.
		ready = []string{"exec", d.name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "app"}
	}
	for start := time.Now(); exec.Command("docker", ready...).Run() != nil; time.Sleep(200 * time.Millisecond) {
		if time.Since(start) > time.Minute {
			d.stop()
			return nil, fmt.Errorf("%s didn't start", kind)
		}
	}
	return d, nil
}

// stop removes the container.
func (d *database) stop() { _ = exec.Command("docker", "rm", "-fv", d.name).Run() }

// psql runs SQL in database app as the owner and returns its output
// (unaligned, tuples only).
func (d *database) psql(sql string) (string, error) {
	out, err := exec.Command("docker", "exec", d.name, "psql", "-U", "postgres", "-d", "app", "-v", "ON_ERROR_STOP=1", "-At", "-c", sql).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("psql: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// redis runs a redis-cli command.
func (d *database) redis(args ...string) (string, error) {
	out, err := exec.Command("docker", append([]string{"exec", d.name, "redis-cli"}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("redis-cli: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
