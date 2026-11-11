// Package resp is the slice of the Redis protocol (RESP2) the data
// panel's Redis engine uses (ADR 0072): commands as arrays of bulk
// strings, pipelined, and replies parsed with caps, so a huge value or a
// hostile server can't make Dock hold more than the caller allows: each
// bulk string is cut at MaxBulk, and a whole reply holds at most
// maxReplyValues values and maxReplyBytes of text. It
// works with Redis 7.0+ and Valkey 7.2+.
//
// A Conn is one connection used by one caller at a time; there is no pool,
// no goroutine and no timer. Deadlines are the caller's
// (NetConn().SetDeadline).
package resp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

// Kind is the type of a reply.
type Kind uint8

// Reply kinds.
const (
	Null Kind = iota
	Status
	Error
	Int
	Bulk
	Array
)

// Value is one reply. Str holds Status, Error and Bulk text (a Bulk cut
// at the connection's MaxBulk, with Len its full length); Int an Int;
// Arr an Array's elements.
type Value struct {
	Kind Kind
	Str  string
	Int  int64
	Arr  []Value
	// Len is a Bulk's full length; it exceeds len(Str) when cut.
	Len int64
}

// Err is the reply as an error when it is one, else nil.
func (v Value) Err() error {
	if v.Kind == Error {
		return &ServerError{Msg: v.Str}
	}
	return nil
}

// Cut reports whether a Bulk was cut at MaxBulk.
func (v Value) Cut() bool { return v.Kind == Bulk && v.Len > int64(len(v.Str)) }

// ServerError is an error reply, e.g. "WRONGPASS invalid username-password
// pair" or "NOPERM ...".
type ServerError struct{ Msg string }

func (e *ServerError) Error() string { return e.Msg }

// Code is the error reply's first word: "WRONGPASS", "NOPERM", "ERR".
func (e *ServerError) Code() string {
	for i := 0; i < len(e.Msg); i++ {
		if e.Msg[i] == ' ' {
			return e.Msg[:i]
		}
	}
	return e.Msg
}

// ErrBadConn means the connection is in an unknown state (a short read, a
// protocol violation, a reply past the caps): close it.
var ErrBadConn = errors.New("redis connection is in an unknown state")

// Limits a Conn reads replies within.
const (
	// DefaultMaxBulk is how much of one bulk string a reply keeps unless
	// the caller sets MaxBulk.
	DefaultMaxBulk = 1 << 20
	// maxArray is the most elements one array may have (Dock's own
	// commands ask for at most 10 000).
	maxArray = 200_000
	// maxReplyValues and maxReplyBytes bound one whole reply, nested
	// arrays and all: a value is ~64 bytes decoded however short it is
	// sent ("*0\r\n" is 4), and every bulk may keep MaxBulk.
	maxReplyValues = 200_000
	maxReplyBytes  = 64 << 20
	// maxDepth is how deeply arrays may nest (streams nest three deep).
	maxDepth = 8
	// maxLine is the longest status, error or header line.
	maxLine = 64 << 10
)

// Conn is one connection.
type Conn struct {
	nc net.Conn
	r  *bufio.Reader
	w  []byte
	// MaxBulk is how much of each bulk string replies keep; 0 means
	// DefaultMaxBulk. The rest is read and dropped.
	MaxBulk int
	broken  bool
	pending int
	// values and bytes are what the reply being read may still hold.
	values, bytes int
}

// New wraps nc.
func New(nc net.Conn) *Conn { return &Conn{nc: nc, r: bufio.NewReaderSize(nc, 16<<10)} }

// NetConn is the underlying connection, for deadlines.
func (c *Conn) NetConn() net.Conn { return c.nc }

// Broken reports whether the connection must be closed.
func (c *Conn) Broken() bool { return c.broken }

// MarkBroken records that the connection's state is unknown.
func (c *Conn) MarkBroken() { c.broken = true }

// Pending is how many sent commands have no reply read yet.
func (c *Conn) Pending() int { return c.pending }

// Close closes the connection.
func (c *Conn) Close() error { return c.nc.Close() }

// Send buffers one command; Flush writes the buffer.
func (c *Conn) Send(args ...string) {
	c.w = append(c.w, '*')
	c.w = strconv.AppendInt(c.w, int64(len(args)), 10)
	c.w = append(c.w, '\r', '\n')
	for _, a := range args {
		c.w = append(c.w, '$')
		c.w = strconv.AppendInt(c.w, int64(len(a)), 10)
		c.w = append(c.w, '\r', '\n')
		c.w = append(c.w, a...)
		c.w = append(c.w, '\r', '\n')
	}
	c.pending++
}

// Flush writes the buffered commands.
func (c *Conn) Flush() error {
	if c.broken {
		return ErrBadConn
	}
	_, err := c.nc.Write(c.w)
	c.w = c.w[:0]
	if err != nil {
		return c.fail(err)
	}
	return nil
}

// Receive reads the next reply. Server errors are Error values, not
// errors; an error means the connection can't be used.
func (c *Conn) Receive() (Value, error) {
	if c.broken {
		return Value{}, ErrBadConn
	}
	c.values, c.bytes = maxReplyValues, maxReplyBytes
	v, err := c.read(0)
	if err != nil {
		return Value{}, c.fail(err)
	}
	c.pending--
	return v, nil
}

// Do sends one command and reads its reply.
func (c *Conn) Do(args ...string) (Value, error) {
	c.Send(args...)
	if err := c.Flush(); err != nil {
		return Value{}, err
	}
	return c.Receive()
}

// Pipeline sends commands at once and reads their replies, in order.
func (c *Conn) Pipeline(cmds ...[]string) ([]Value, error) {
	for _, cmd := range cmds {
		c.Send(cmd...)
	}
	if err := c.Flush(); err != nil {
		return nil, err
	}
	out := make([]Value, len(cmds))
	for i := range out {
		v, err := c.Receive()
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (c *Conn) fail(err error) error {
	c.broken = true
	if errors.Is(err, ErrBadConn) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrBadConn, err)
}

// errTooBig is a reply past maxReplyValues or maxReplyBytes.
var errTooBig = errors.New("reply too big")

// read parses one reply at nesting depth, within what the reply may still
// hold (c.values, c.bytes).
func (c *Conn) read(depth int) (Value, error) {
	if c.values--; c.values < 0 {
		return Value{}, errTooBig
	}
	line, err := c.line()
	if err != nil {
		return Value{}, err
	}
	if len(line) == 0 {
		return Value{}, errors.New("empty reply line")
	}
	body := line[1:]
	switch line[0] {
	case '+', '-':
		if c.bytes -= len(body); c.bytes < 0 {
			return Value{}, errTooBig
		}
	}
	switch line[0] {
	case '+':
		return Value{Kind: Status, Str: string(body)}, nil
	case '-':
		return Value{Kind: Error, Str: string(body)}, nil
	case ':':
		n, err := strconv.ParseInt(string(body), 10, 64)
		if err != nil {
			return Value{}, fmt.Errorf("bad integer reply %q", body)
		}
		return Value{Kind: Int, Int: n}, nil
	case '$':
		n, err := strconv.ParseInt(string(body), 10, 64)
		if err != nil || n < -1 {
			return Value{}, fmt.Errorf("bad bulk length %q", body)
		}
		if n == -1 {
			return Value{Kind: Null}, nil
		}
		return c.bulk(n)
	case '*':
		n, err := strconv.ParseInt(string(body), 10, 64)
		if err != nil || n < -1 || n > maxArray {
			return Value{}, fmt.Errorf("bad array length %q", body)
		}
		if n == -1 {
			return Value{Kind: Null}, nil
		}
		if depth == maxDepth {
			return Value{}, errors.New("reply nested too deep")
		}
		// Grown as elements arrive: a length alone allocates nothing.
		arr := make([]Value, 0, min(n, 1024))
		for range n {
			v, err := c.read(depth + 1)
			if err != nil {
				return Value{}, err
			}
			arr = append(arr, v)
		}
		return Value{Kind: Array, Arr: arr}, nil
	}
	return Value{}, fmt.Errorf("unknown reply type %q", line[0])
}

// bulk reads a bulk string of n bytes, keeping at most MaxBulk.
func (c *Conn) bulk(n int64) (Value, error) {
	keep := int64(c.MaxBulk)
	if keep <= 0 {
		keep = DefaultMaxBulk
	}
	keep = min(keep, n)
	if c.bytes -= int(keep); c.bytes < 0 {
		return Value{}, errTooBig
	}
	buf := make([]byte, keep)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return Value{}, err
	}
	if _, err := io.CopyN(io.Discard, c.r, n-keep); err != nil {
		return Value{}, err
	}
	var crlf [2]byte
	if _, err := io.ReadFull(c.r, crlf[:]); err != nil {
		return Value{}, err
	}
	if crlf != [2]byte{'\r', '\n'} {
		return Value{}, errors.New("bulk string not ended by CRLF")
	}
	return Value{Kind: Bulk, Str: string(buf), Len: n}, nil
}

// line reads one CRLF-terminated line, without the CRLF.
func (c *Conn) line() ([]byte, error) {
	line, err := c.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, errors.New("reply line too long")
	}
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' || len(line) > maxLine {
		return nil, errors.New("reply line not ended by CRLF")
	}
	return line[:len(line)-2], nil
}
