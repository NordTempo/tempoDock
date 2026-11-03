// Command dock-probe is the health probe Dock runs inside app containers
// (ADR 0043). Docker runs it as the container's healthcheck, so the probe
// works in any image (no shell, curl or wget needed) and the daemon never
// polls. dock embeds it (internal/node/probe), writes it to its data dir
// and bind-mounts it read-only into each container with a check.
//
//	dock-probe tcp PORT TIMEOUT_MS
//	dock-probe http PORT PATH TIMEOUT_MS
//	dock-probe version
//
// It connects to PORT on the container's loopback, then, if nothing
// listens there, on the container's other addresses (an app bound to its
// network address only). The http probe sends one GET for PATH and passes
// on a 2xx or 3xx status. Exit status 0 is healthy, 1 unhealthy; the one
// line it prints says why, and Docker keeps it in the health log.
//
// It imports only net, os, strconv and time (no net/http, no fmt) to stay
// small: Docker execs it every interval in the app's own cgroup.
package main

import (
	"errors"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
)

// version is what `dock-probe version` prints; dock checks the probe can
// run on the host with it before using it.
const version = "dock-probe 1"

func main() {
	err := run(os.Args[1:])
	if err != nil {
		_, _ = os.Stdout.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

// run parses the arguments and runs one probe.
func run(args []string) error {
	if len(args) == 1 && args[0] == "version" {
		_, _ = os.Stdout.WriteString(version + "\n")
		return nil
	}
	if len(args) < 3 {
		return errors.New("usage: dock-probe tcp PORT TIMEOUT_MS | http PORT PATH TIMEOUT_MS")
	}
	port, err := strconv.Atoi(args[1])
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid port " + args[1])
	}
	ms, err := strconv.Atoi(args[len(args)-1])
	if err != nil || ms < 1 {
		return errors.New("invalid timeout " + args[len(args)-1])
	}
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	switch {
	case args[0] == "tcp" && len(args) == 3:
		conn, err := dial(port, deadline)
		if err != nil {
			return err
		}
		return conn.Close()
	case args[0] == "http" && len(args) == 4:
		return probeHTTP(port, args[2], deadline)
	}
	return errors.New("usage: dock-probe tcp PORT TIMEOUT_MS | http PORT PATH TIMEOUT_MS")
}

// dial connects to port on loopback, or on the container's other
// addresses when nothing listens on loopback.
func dial(port int, deadline time.Time) (net.Conn, error) {
	p := strconv.Itoa(port)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", p), time.Until(deadline))
	if err == nil || !errors.Is(err, syscall.ECONNREFUSED) {
		return conn, describe("connect to port "+p, err)
	}
	first := err
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ip, ok := a.(*net.IPNet)
		if !ok || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
			continue
		}
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip.IP.String(), p), time.Until(deadline)); err == nil {
			return conn, nil
		}
	}
	return nil, describe("connect to port "+p, first)
}

// probeHTTP sends GET path and reads the status line.
func probeHTTP(port int, path string, deadline time.Time) error {
	conn, err := dial(port, deadline)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	host := "127.0.0.1:" + strconv.Itoa(port)
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\nUser-Agent: dock-probe\r\nAccept: */*\r\nConnection: close\r\n\r\n"
	what := "GET " + path
	if _, err := conn.Write([]byte(req)); err != nil {
		return describe(what, err)
	}
	// "HTTP/1.1 200 OK": the status is bytes 9-11. One read usually holds
	// the whole line; keep reading until it does.
	buf := make([]byte, 0, 64)
	for len(buf) < 12 {
		n, err := conn.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err != nil && len(buf) < 12 {
			if len(buf) == 0 && !errors.Is(err, os.ErrDeadlineExceeded) {
				return errors.New(what + ": connection closed without an answer")
			}
			return describe(what, err)
		}
	}
	if string(buf[:5]) != "HTTP/" || buf[8] != ' ' {
		return errors.New(what + ": the answer is not HTTP")
	}
	code, err := strconv.Atoi(string(buf[9:12]))
	if err != nil {
		return errors.New(what + ": the answer is not HTTP")
	}
	if code < 200 || code >= 400 {
		return errors.New(what + " returned " + strconv.Itoa(code))
	}
	return nil
}

// describe turns a network error into a short reason.
func describe(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrDeadlineExceeded):
		return errors.New(what + ": timed out")
	case errors.Is(err, syscall.ECONNREFUSED):
		return errors.New(what + ": connection refused, nothing listens on it")
	case errors.Is(err, syscall.ECONNRESET):
		return errors.New(what + ": connection reset")
	}
	return errors.New(what + ": " + err.Error())
}
