// Command dock-probe is the health probe Dock runs inside app containers
// (ADR 0043). Docker runs it as the container's healthcheck, so the probe
// works in any image (no shell, curl or wget needed) and the daemon never
// polls. dock embeds it (internal/node/probe), writes it to its data dir
// and bind-mounts it read-only into each container with a check.
//
//	dock-probe tcp PORT TIMEOUT_MS
//	dock-probe http PORT PATH TIMEOUT_MS
//	dock-probe pg PORT TIMEOUT_MS
//	dock-probe relay ADDR SOCKET
//	dock-probe wal-push ADDR PATH FILE
//	dock-probe version
//
// It connects to PORT on the container's loopback, then, if nothing
// listens there, on the container's other addresses (an app bound to its
// network address only). The http probe sends one GET for PATH and passes
// on a 2xx or 3xx status. The pg probe sends a PostgreSQL startup message
// and fails while the server answers that it can't take connections yet
// (SQLSTATE 57P03: starting up, shutting down or recovering); any other
// answer passes (internal/node/pgready). Exit status 0 is healthy, 1
// unhealthy; the one line it prints says why, and Docker keeps it in the
// health log.
//
// relay is the BuildKit container's way to the platform registry (ADR
// 0089 amendment): it listens on ADDR (the registry's 127.0.0.1:PORT,
// in the container's own network namespace), joins each connection to
// the Unix socket SOCKET, which the dock daemon serves on the host, and
// prints "ready" once it listens. It runs only while a build exports,
// and exits when its standard input closes (the daemon ends the exec).
//
// wal-push is a Postgres database's archive_command while point-in-time
// restore is on (ADR 0093): it sends the finished WAL file at PATH (%p)
// as FILE (%f) to the database's archiver at ADDR, and exits 0 only once
// the archiver answers that the file is stored off the server. The
// archiver holds the storage keys; the database's container never does.
//
// It imports only io, net, os, strconv and time, and pgready, which
// imports no more (no net/http, no fmt), to stay small: Docker execs it every interval in the app's own cgroup.
package main

import (
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/internal/node/pgready"
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
	if len(args) == 4 && args[0] == "wal-push" {
		return walPush(args[1], args[2], args[3])
	}
	if len(args) == 3 && args[0] == "relay" {
		return relay(args[1], args[2], os.Stdin)
	}
	if len(args) < 3 {
		return errors.New(usage)
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
	case args[0] == "pg" && len(args) == 3:
		return probePG(port, deadline)
	}
	return errors.New(usage)
}

// usage is the error for arguments that name no probe.
const usage = "usage: dock-probe tcp PORT TIMEOUT_MS | http PORT PATH TIMEOUT_MS | pg PORT TIMEOUT_MS"

// probePG asks the PostgreSQL server on port whether it accepts
// connections (pgready.Check).
func probePG(port int, deadline time.Time) error {
	conn, err := dial(port, deadline)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	err = pgready.Check(conn)
	if _, ok := errors.AsType[*pgready.NotReadyError](err); ok {
		return err
	}
	return describe("PostgreSQL on port "+strconv.Itoa(port), err)
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
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\nUser-Agent: dock-probe\r\nAccept: */*\r\n" +
		"traceparent: " + traceparent(uint64(time.Now().UnixNano())^uint64(os.Getpid())<<40) + "\r\nConnection: close\r\n\r\n"
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

// traceparent is the W3C trace context the probe sends: a trace ID that
// starts with "dockprbe" (internal/traces.ProbeTraceHi). An app's
// OpenTelemetry SDK continues the trace, and Dock's ingest knows every
// span of it for a probe's: it never samples one, but keeps one that
// fails, so a failing health check can be read like any other request.
func traceparent(random uint64) string {
	return "00-646f636b70726265" + hex16(random) + "-" + hex16(random|1) + "-01"
}

// hex16 is v as 16 hex digits.
func hex16(v uint64) string {
	s := strconv.FormatUint(v, 16)
	for len(s) < 16 {
		s = "0" + s
	}
	return s
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
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New(what + ": connection closed without a whole answer")
	}
	return errors.New(what + ": " + err.Error())
}

// relay serves addr until stdin ends, joining each connection to the
// Unix socket sock.
func relay(addr, sock string, stdin io.Reader) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		_ = ln.Close()
	}()
	_, _ = os.Stdout.WriteString("ready\n")
	for {
		c, err := ln.Accept()
		if err != nil {
			return nil // stdin ended
		}
		go join(c, sock)
	}
}

// join copies between c and a new connection to sock until both sides
// are done, passing on each side's end of stream.
func join(c net.Conn, sock string) {
	defer func() { _ = c.Close() }()
	u, err := net.Dial("unix", sock)
	if err != nil {
		return
	}
	defer func() { _ = u.Close() }()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(u, c)
		_ = u.(*net.UnixConn).CloseWrite()
		close(done)
	}()
	_, _ = io.Copy(c, u)
	_ = c.(*net.TCPConn).CloseWrite()
	<-done
}

// walPushTimeout bounds one archive_command: connecting, sending a 16 MB
// segment and the archiver's upload of it. Postgres retries one that
// fails.
const walPushTimeout = 10 * time.Minute

// walPush sends the file at path to the archiver at addr as
// `PUT /wal/<file>` (HTTP/1.0, just enough of it) with its length and
// mtime, and reads the answer: 200, or the archiver's error.
func walPush(addr, path, file string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return describe("connect to the WAL archiver at "+addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(walPushTimeout))
	what := "archive " + file
	head := "PUT /wal/" + file + " HTTP/1.0\r\nHost: dock-wal\r\nContent-Length: " + strconv.FormatInt(info.Size(), 10) +
		"\r\nX-Dock-Mtime: " + strconv.FormatInt(info.ModTime().UnixMilli(), 10) + "\r\n\r\n"
	if _, err := conn.Write([]byte(head)); err != nil {
		return describe(what, err)
	}
	if _, err := io.CopyN(conn, f, info.Size()); err != nil {
		return describe(what, err)
	}
	// HTTP/1.0: the archiver closes the connection after its answer.
	answer, err := io.ReadAll(io.LimitReader(conn, 4096))
	if err != nil && len(answer) == 0 {
		return describe(what, err)
	}
	if len(answer) < 12 || string(answer[:5]) != "HTTP/" {
		return errors.New(what + ": the archiver's answer is not HTTP")
	}
	if string(answer[9:12]) == "200" {
		return nil
	}
	body := string(answer)
	for i := 0; i+3 < len(body); i++ {
		if body[i:i+4] == "\r\n\r\n" {
			body = body[i+4:]
			break
		}
	}
	for len(body) > 0 && (body[len(body)-1] == '\n' || body[len(body)-1] == '\r') {
		body = body[:len(body)-1]
	}
	return errors.New(what + ": the archiver answered " + string(answer[9:12]) + ": " + body)
}
