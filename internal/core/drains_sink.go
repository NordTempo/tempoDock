package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/version"
)

// How each kind of drain encodes and sends a batch (ADR 0046):
//
//   - http: POST application/x-ndjson, one JSON object per line with
//     time, stream, text, project, environment, service, release, server.
//   - loki: POST /loki/api/v1/push (JSON), one stream per service and
//     output stream, labelled project, environment, service, stream.
//   - syslog: RFC 5424 messages, APP-NAME the service, MSGID the stream,
//     HOSTNAME the server, severity 6 (stdout) or 3 (stderr) of facility
//     user. tcp and tls frame them with octet counting (RFC 6587), udp
//     sends one per datagram.

// lokiPushPath is Loki's push API, appended to a drain's base URL.
const lokiPushPath = "/loki/api/v1/push"

// deliver makes one attempt at sending a batch to d. buf is reused
// between batches by the drain's sender; nil allocates one.
func (m *drainManager) deliver(ctx context.Context, d *drain, batch []drainRecord, buf *bytes.Buffer) error {
	if buf == nil {
		buf = new(bytes.Buffer)
	}
	ctx, cancel := context.WithTimeout(ctx, drainSendTimeout)
	defer cancel()
	buf.Reset()
	switch d.kind {
	case apitypes.DrainHTTP:
		buf.Write(appendNDJSON(buf.AvailableBuffer(), batch))
		return m.post(ctx, d, "application/x-ndjson", buf.Bytes())
	case apitypes.DrainLoki:
		buf.Write(appendLoki(buf.AvailableBuffer(), batch))
		return m.post(ctx, d, "application/json", buf.Bytes())
	case apitypes.DrainSyslog:
		return m.sendSyslog(ctx, d, batch, buf)
	}
	return fmt.Errorf("unknown log drain kind %q", d.kind)
}

// post sends a body to an http or Loki drain; anything but a 2xx answer
// is an error. A user and password in the URL go as basic auth (the HTTP
// client sets it); the token as a bearer token.
func (m *drainManager) post(ctx context.Context, d *drain, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "dock/"+version.Version)
	for k, v := range d.cfg.Headers {
		req.Header.Set(k, v)
	}
	if d.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL may hold a password; don't repeat it
		}
		return fmt.Errorf("POST to the drain's URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		// Only the status: the body would let whoever adds a drain read
		// what a server answers to the control plane.
		return fmt.Errorf("the drain's URL answered %s", resp.Status)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// appendNDJSON appends a batch as JSON lines.
func appendNDJSON(b []byte, batch []drainRecord) []byte {
	for _, r := range batch {
		b = append(b, `{"time":"`...)
		b = r.Time.UTC().AppendFormat(b, time.RFC3339Nano)
		b = append(b, `","stream":`...)
		b = appendJSONString(b, r.Stream)
		b = append(b, `,"text":`...)
		b = appendJSONString(b, r.Text)
		b = append(b, r.src.ndjson...)
	}
	return b
}

// lokiStream is one Loki stream in a batch: a source's stdout or stderr.
type lokiStream struct {
	src    *logSource
	stream string
}

// appendLoki appends a batch as a Loki push request: a stream per
// source and output stream, each with its lines in order.
func appendLoki(b []byte, batch []drainRecord) []byte {
	streams := make([]lokiStream, 0, 4)
	for _, r := range batch {
		k := lokiStream{r.src, r.Stream}
		found := false
		for _, s := range streams {
			if s == k {
				found = true
				break
			}
		}
		if !found {
			streams = append(streams, k)
		}
	}
	b = append(b, `{"streams":[`...)
	for i, s := range streams {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"stream":{"project":`...)
		b = appendJSONString(b, s.src.Project)
		b = append(b, `,"environment":`...)
		b = appendJSONString(b, s.src.Environment)
		b = append(b, `,"service":`...)
		b = appendJSONString(b, s.src.Service)
		b = append(b, `,"stream":`...)
		b = appendJSONString(b, s.stream)
		b = append(b, `},"values":[`...)
		first := true
		for _, r := range batch {
			if r.src != s.src || r.Stream != s.stream {
				continue
			}
			if !first {
				b = append(b, ',')
			}
			first = false
			b = append(b, `["`...)
			b = strconv.AppendInt(b, r.Time.UnixNano(), 10)
			b = append(b, `",`...)
			b = appendJSONString(b, r.Text)
			b = append(b, ']')
		}
		b = append(b, "]}"...)
	}
	return append(b, "]}"...)
}

// appendJSONString appends s as a JSON string. Invalid UTF-8 becomes
// U+FFFD, as encoding/json does.
func appendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `�`...)
			i++
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

// Syslog severities of facility user (1): info for stdout, err for stderr.
const (
	syslogStdout = 1*8 + 6
	syslogStderr = 1*8 + 3
	// maxSyslogDatagram keeps a udp message within one datagram.
	maxSyslogDatagram = 65_000
	// syslogConnIdle is how long an unused tcp or tls connection is
	// kept: past it the next batch dials again. Within it, the
	// connection's watcher tells when the server closed it first.
	syslogConnIdle = time.Minute
)

// appendSyslog appends one RFC 5424 message (without framing).
func appendSyslog(b []byte, r drainRecord) []byte {
	pri := syslogStdout
	if r.Stream == "stderr" {
		pri = syslogStderr
	}
	b = append(b, '<')
	b = strconv.AppendInt(b, int64(pri), 10)
	b = append(b, ">1 "...)
	b = r.Time.UTC().AppendFormat(b, "2006-01-02T15:04:05.000000Z07:00")
	b = append(b, ' ')
	b = appendSyslogField(b, r.src.host, 255)
	b = append(b, ' ')
	b = appendSyslogField(b, r.src.Service, 48)
	b = append(b, " - "...)
	b = appendSyslogField(b, r.Stream, 32)
	b = append(b, " - "...)
	return append(b, r.Text...)
}

// appendSyslogField appends a header field: printable ASCII, at most
// limit bytes, "-" when empty (RFC 5424 section 6).
func appendSyslogField(b []byte, s string, limit int) []byte {
	if s == "" {
		return append(b, '-')
	}
	for i := 0; i < len(s) && i < limit; i++ {
		c := s[i]
		if c < 33 || c > 126 {
			c = '_'
		}
		b = append(b, c)
	}
	return b
}

// syslogConn is a syslog drain's connection, kept between batches and
// used by one sender (or a test) at a time. gone is closed once a tcp or
// tls connection's server closed or reset it (watchSyslog); nil for udp.
type syslogConn struct {
	mu   sync.Mutex
	conn net.Conn
	gone chan struct{}
	used time.Time
}

// watchSyslog reads a tcp or tls syslog connection until it fails, then
// closes gone. Syslog servers never send, so a read ending is the server
// closing the connection, which a write wouldn't say until the batch
// after the one it lost. Reading through tls handles the session tickets
// a server sends after the handshake. It blocks in the read, costing
// nothing while idle, and ends when the connection is closed (the drain
// stopping closes it); it touches nothing else, so c.wg doesn't count it.
func watchSyslog(conn net.Conn, gone chan<- struct{}) {
	defer close(gone)
	_, _ = io.Copy(io.Discard, conn)
}

// closed reports whether the kept connection's server closed it.
func (s *syslogConn) closed() bool {
	select {
	case <-s.gone:
		return true
	default:
		return false
	}
}

// close closes the connection, if any; the drain's context ending calls it.
func (s *syslogConn) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// sendSyslog sends a batch to a syslog drain: one write of octet-counted
// frames over tcp or tls, a datagram per line over udp. A failed write
// drops the connection; the next attempt dials again.
func (m *drainManager) sendSyslog(ctx context.Context, d *drain, batch []drainRecord, buf *bytes.Buffer) error {
	sc := &d.conn
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if d.ctx.Err() != nil {
		return d.ctx.Err()
	}
	u, err := url.Parse(d.cfg.URL)
	if err != nil {
		return err
	}
	if sc.conn != nil && (time.Since(sc.used) > syslogConnIdle || sc.closed()) {
		_ = sc.conn.Close()
		sc.conn = nil
	}
	if sc.conn == nil {
		if sc.conn, err = m.dialSyslog(ctx, u); err != nil {
			return err
		}
		sc.gone = nil
		if u.Scheme != "udp" {
			sc.gone = make(chan struct{})
			go watchSyslog(sc.conn, sc.gone)
		}
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = sc.conn.SetWriteDeadline(dl)
	}
	// A receiver that stops reading fills the socket's buffers and blocks
	// the write; removing the drain or closing the daemon ends it then,
	// not at the deadline (close can't: it waits for sc.mu).
	conn := sc.conn
	defer context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Unix(1, 0)) })()
	msg := make([]byte, 0, 512)
	if u.Scheme == "udp" {
		for _, r := range batch {
			msg = appendSyslog(msg[:0], r)
			if len(msg) > maxSyslogDatagram {
				msg = msg[:maxSyslogDatagram]
			}
			if _, err = sc.conn.Write(msg); err != nil {
				break
			}
		}
	} else {
		for _, r := range batch {
			msg = appendSyslog(msg[:0], r)
			buf.Write(strconv.AppendInt(buf.AvailableBuffer(), int64(len(msg)), 10))
			buf.WriteByte(' ')
			buf.Write(msg)
		}
		_, err = sc.conn.Write(buf.Bytes())
	}
	if err != nil {
		_ = sc.conn.Close()
		sc.conn = nil
		return fmt.Errorf("send to %s: %w", u.Host, err)
	}
	sc.used = time.Now()
	return nil
}

// dialSyslog connects to a syslog server, refusing addresses the guard
// refuses (after DNS).
func (m *drainManager) dialSyslog(ctx context.Context, u *url.URL) (net.Conn, error) {
	d := &net.Dialer{Timeout: drainSendTimeout, Control: m.guard.control}
	var conn net.Conn
	var err error
	switch u.Scheme {
	case "tls":
		cfg := &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
		conn, err = (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", u.Host)
	case "tcp", "udp":
		conn, err = d.DialContext(ctx, u.Scheme, u.Host)
	default:
		return nil, fmt.Errorf("unknown syslog scheme %q", u.Scheme)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", u.Host, err)
	}
	return conn, nil
}
