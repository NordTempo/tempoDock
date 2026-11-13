// Package mailtest is an SMTP server for tests: it listens on 127.0.0.1,
// takes mail without TLS or a login, and records what it was sent. It can
// refuse a recipient or answer every connection with 421, so tests cover
// what a real server does without ever reaching a real mailbox.
package mailtest

import (
	"bufio"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Message is one mail the server took.
type Message struct {
	From string
	To   []string
	Data string // headers and body as sent, CRLF line ends
}

// Server is a running test server.
type Server struct {
	Host string
	Port int

	ln       net.Listener
	mu       sync.Mutex
	msgs     []Message
	reject   map[string]string // recipient: the answer to its RCPT
	down     bool
	received chan struct{}
}

// Start starts a server, closed when the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	s := &Server{Host: "127.0.0.1", Port: addr.Port, ln: ln, reject: map[string]string{}, received: make(chan struct{}, 1024)}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// Messages returns what the server took so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Received is signalled once per message the server takes.
func (s *Server) Received() <-chan struct{} { return s.received }

// Reject makes RCPT TO for addr answer with reply, like "550 5.1.1 no
// such user".
func (s *Server) Reject(addr, reply string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reject[strings.ToLower(addr)] = reply
}

// Down makes every new connection get "421 try later" (down) or be
// served again.
func (s *Server) Down(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

// handle speaks just enough SMTP for net/smtp's client.
func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	tp := textproto.NewConn(conn)
	s.mu.Lock()
	down := s.down
	s.mu.Unlock()
	if down {
		_ = tp.PrintfLine("421 4.3.2 mailtest is down")
		return
	}
	_ = tp.PrintfLine("220 mailtest ready")
	var cur Message
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			_ = tp.PrintfLine("250 mailtest")
		case "MAIL":
			cur = Message{From: addrOf(arg)}
			_ = tp.PrintfLine("250 ok")
		case "RCPT":
			to := addrOf(arg)
			s.mu.Lock()
			reply, refused := s.reject[strings.ToLower(to)]
			s.mu.Unlock()
			if refused {
				_ = tp.PrintfLine("%s", reply)
				continue
			}
			cur.To = append(cur.To, to)
			_ = tp.PrintfLine("250 ok")
		case "DATA":
			_ = tp.PrintfLine("354 go ahead")
			data, err := readDot(tp.R)
			if err != nil {
				return
			}
			cur.Data = data
			s.mu.Lock()
			s.msgs = append(s.msgs, cur)
			s.mu.Unlock()
			s.received <- struct{}{}
			_ = tp.PrintfLine("250 ok: queued as %s", strconv.Itoa(len(s.Messages())))
		case "RSET":
			cur = Message{}
			_ = tp.PrintfLine("250 ok")
		case "NOOP":
			_ = tp.PrintfLine("250 ok")
		case "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("502 not implemented")
		}
	}
}

// readDot reads DATA up to the lone dot, keeping CRLF line ends and
// undoing dot-stuffing.
func readDot(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" {
			return b.String(), nil
		}
		b.WriteString(strings.TrimPrefix(line, "."))
	}
}

// addrOf takes the address out of "FROM:<a@b> SIZE=1" or "TO:<a@b>".
func addrOf(arg string) string {
	if i := strings.IndexByte(arg, '<'); i >= 0 {
		if j := strings.IndexByte(arg[i:], '>'); j > 0 {
			return arg[i+1 : i+j]
		}
	}
	return arg
}
