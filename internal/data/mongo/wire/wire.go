package wire

import (
	"bufio"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
)

// opMsg is OP_MSG, the one opcode MongoDB 6.0+ needs.
const opMsg = 2013

// Limits a Conn works within.
const (
	// DefaultMaxReply is the largest reply a Conn reads unless the caller
	// sets MaxReply: a little over the server's 16 MB document limit.
	DefaultMaxReply = 17 << 20
	// maxRequest is the largest command a Conn sends.
	maxRequest = 16 << 20
)

// ErrBadConn means the connection is in an unknown state (a short read, a
// reply past the cap, a protocol violation): close it.
var ErrBadConn = errors.New("mongodb connection is in an unknown state")

// ServerError is a command's {ok: 0} reply.
type ServerError struct {
	Code     int32
	CodeName string
	Msg      string
}

func (e *ServerError) Error() string {
	if e.CodeName != "" {
		return e.Msg + " (" + e.CodeName + ")"
	}
	return e.Msg
}

// Server error codes the engine tells apart.
const (
	CodeUnauthorized  = 13
	CodeAuthFailed    = 18
	CodeNamespaceGone = 26
	CodeUserNotFound  = 11
	CodeDuplicateUser = 51003
	CodeMaxTimeMS     = 50
	CodeInterrupted   = 11601
	CodeCommandAbsent = 59
)

// Conn is one connection.
type Conn struct {
	nc net.Conn
	r  *bufio.Reader
	// MaxReply is the largest reply read; 0 means DefaultMaxReply. A
	// larger one breaks the connection.
	MaxReply int
	broken   bool
	next     int32
	// LastReply is the size in bytes of the last reply read, for callers
	// that keep a budget across replies.
	LastReply int
	buf       []byte
}

// New wraps nc.
func New(nc net.Conn) *Conn { return &Conn{nc: nc, r: bufio.NewReaderSize(nc, 16<<10)} }

// NetConn is the underlying connection, for deadlines.
func (c *Conn) NetConn() net.Conn { return c.nc }

// Broken reports whether the connection must be closed.
func (c *Conn) Broken() bool { return c.broken }

// Close closes the connection.
func (c *Conn) Close() error { return c.nc.Close() }

// Run sends cmd to db ($db is added) and reads its reply. A reply with
// ok: 0 is a *ServerError; a connection that can't be trusted afterwards
// is broken and the error wraps ErrBadConn.
func (c *Conn) Run(db string, cmd D) (D, error) {
	if c.broken {
		return nil, ErrBadConn
	}
	body := append(append(D{}, cmd...), E{Key: "$db", Value: db})
	c.next++
	id := c.next
	b := c.buf[:0]
	b = append(b, make([]byte, 16)...)
	b = binary.LittleEndian.AppendUint32(b, 0) // flags
	b = append(b, 0)                           // section kind 0: the body
	b, err := AppendDoc(b, body)
	if err != nil {
		return nil, err
	}
	if len(b) > maxRequest {
		return nil, fmt.Errorf("the command is %d MB, more than %d MB", len(b)>>20, maxRequest>>20)
	}
	binary.LittleEndian.PutUint32(b[0:], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[4:], uint32(id))
	binary.LittleEndian.PutUint32(b[12:], opMsg)
	c.buf = b
	if _, err := c.nc.Write(b); err != nil {
		c.broken = true
		return nil, fmt.Errorf("%w: %w", ErrBadConn, err)
	}
	reply, err := c.read(id)
	if err != nil {
		c.broken = true
		return nil, fmt.Errorf("%w: %w", ErrBadConn, err)
	}
	if ok, _ := reply.Get("ok"); !truthy(ok) {
		return reply, serverError(reply)
	}
	return reply, nil
}

// read reads the reply to request id: one OP_MSG with a body section.
func (c *Conn) read(id int32) (D, error) {
	var h [16]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(h[0:]))
	limit := c.MaxReply
	if limit == 0 {
		limit = DefaultMaxReply
	}
	if n < 21 || n > limit {
		return nil, fmt.Errorf("a reply of %d bytes is past the %d MB limit", n, limit>>20)
	}
	if int32(binary.LittleEndian.Uint32(h[8:])) != id || binary.LittleEndian.Uint32(h[12:]) != opMsg {
		return nil, errors.New("a reply to another request")
	}
	c.LastReply = n
	msg := make([]byte, n-16)
	if _, err := io.ReadFull(c.r, msg); err != nil {
		return nil, err
	}
	flags := binary.LittleEndian.Uint32(msg)
	// The low 16 bits are required: a flag Dock doesn't know there means
	// a reply it can't read right.
	if flags&0xFFFC != 0 {
		return nil, fmt.Errorf("a reply with flags 0x%x Dock doesn't know", flags&0xFFFF)
	}
	if flags&1 != 0 { // checksumPresent
		msg = msg[:len(msg)-4]
	}
	if flags&2 != 0 { // moreToCome: Dock never asks for it
		return nil, errors.New("an unrequested exhaust reply")
	}
	if len(msg) < 5 || msg[4] != 0 {
		return nil, errors.New("a reply without its body")
	}
	return ReadDoc(msg[5:])
}

func truthy(v any) bool {
	switch v := v.(type) {
	case float64:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case bool:
		return v
	}
	return false
}

func serverError(reply D) *ServerError {
	e := &ServerError{}
	switch v, _ := reply.Get("code"); n := v.(type) {
	case int32:
		e.Code = n
	case int64:
		e.Code = int32(n)
	case float64:
		e.Code = int32(n)
	}
	e.CodeName, _ = getStr(reply, "codeName")
	e.Msg, _ = getStr(reply, "errmsg")
	if e.Msg == "" {
		e.Msg = "command failed"
	}
	return e
}

// Hello is what the server said in the handshake.
type Hello struct {
	// Version is the server's version ("8.0.38"), from buildInfo.
	Version string
	// MaxWireVersion is the newest protocol it speaks (17 is 6.0).
	MaxWireVersion int32
}

// MinWireVersion is MongoDB 6.0's.
const MinWireVersion = 17

// Handshake sends hello with the client's name, then logs in as user
// with SCRAM-SHA-256 against authDB (admin for Dock's users) unless
// user is empty, and reads the server's version.
func (c *Conn) Handshake(app, user, password, authDB string) (Hello, error) {
	var h Hello
	hello, err := c.Run("admin", D{
		{Key: "hello", Value: int32(1)},
		{Key: "client", Value: D{{Key: "application", Value: D{{Key: "name", Value: app}}}, {Key: "driver", Value: D{{Key: "name", Value: "tempo-dock"}, {Key: "version", Value: "1"}}},
			{Key: "os", Value: D{{Key: "type", Value: runtime.GOOS}}}}},
	})
	if err != nil {
		return h, err
	}
	switch v, _ := hello.Get("maxWireVersion"); n := v.(type) {
	case int32:
		h.MaxWireVersion = n
	case int64:
		h.MaxWireVersion = int32(n)
	}
	if h.MaxWireVersion < MinWireVersion {
		return h, fmt.Errorf("the server speaks wire version %d; MongoDB 6.0 or later is needed", h.MaxWireVersion)
	}
	if user != "" {
		if err := c.scram(user, password, authDB); err != nil {
			return h, err
		}
	}
	info, err := c.Run("admin", D{{Key: "buildInfo", Value: int32(1)}})
	if err != nil {
		return h, err
	}
	h.Version, _ = getStr(info, "version")
	return h, nil
}

// scram logs in with SCRAM-SHA-256 (RFC 7677), the password prepared
// with SASLprep (saslPrep) as the server prepares it.
func (c *Conn) scram(user, password, db string) error {
	nonce := make([]byte, 24)
	_, _ = rand.Read(nonce)
	cnonce := base64.StdEncoding.EncodeToString(nonce)
	name := strings.NewReplacer("=", "=3D", ",", "=2C").Replace(user)
	first := "n=" + name + ",r=" + cnonce
	start, err := c.Run(db, D{
		{Key: "saslStart", Value: int32(1)}, {Key: "mechanism", Value: "SCRAM-SHA-256"},
		{Key: "payload", Value: Binary{Data: []byte("n,," + first)}}, {Key: "options", Value: D{{Key: "skipEmptyExchange", Value: true}}},
	})
	if err != nil {
		return err
	}
	conv, _ := start.Get("conversationId")
	payload, _ := start.Get("payload")
	sb, _ := payload.(Binary)
	attrs := scramAttrs(string(sb.Data))
	salt, err := base64.StdEncoding.DecodeString(attrs["s"])
	iter := 0
	_, _ = fmt.Sscanf(attrs["i"], "%d", &iter)
	if err != nil || iter < 4096 || iter > 1<<20 || !strings.HasPrefix(attrs["r"], cnonce) {
		return errors.New("the server's SCRAM challenge isn't valid")
	}
	prepared, err := saslPrep(password)
	if err != nil {
		return err
	}
	salted, err := pbkdf2.Key(sha256.New, prepared, salt, iter, 32)
	if err != nil {
		return err
	}
	mac := func(key []byte, s string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(s))
		return m.Sum(nil)
	}
	clientKey := mac(salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	final := "c=biws,r=" + attrs["r"]
	authMsg := first + "," + string(sb.Data) + "," + final
	sig := mac(stored[:], authMsg)
	proof := make([]byte, len(clientKey))
	for i := range clientKey {
		proof[i] = clientKey[i] ^ sig[i]
	}
	cont, err := c.Run(db, D{
		{Key: "saslContinue", Value: int32(1)}, {Key: "conversationId", Value: conv},
		{Key: "payload", Value: Binary{Data: []byte(final + ",p=" + base64.StdEncoding.EncodeToString(proof))}},
	})
	if err != nil {
		return err
	}
	payload, _ = cont.Get("payload")
	fb, _ := payload.(Binary)
	want := base64.StdEncoding.EncodeToString(mac(mac(salted, "Server Key"), authMsg))
	if !hmac.Equal([]byte(scramAttrs(string(fb.Data))["v"]), []byte(want)) {
		return errors.New("the server's SCRAM signature doesn't match: not the server Dock logged in to")
	}
	if done, _ := cont.Get("done"); done != true {
		// Without skipEmptyExchange support the server wants one more round.
		last, err := c.Run(db, D{{Key: "saslContinue", Value: int32(1)}, {Key: "conversationId", Value: conv}, {Key: "payload", Value: Binary{}}})
		if err != nil {
			return err
		}
		if done, _ := last.Get("done"); done != true {
			return errors.New("the server didn't finish the SCRAM login")
		}
	}
	return nil
}

// scramAttrs splits "r=...,s=...,i=4096".
func scramAttrs(s string) map[string]string {
	out := map[string]string{}
	for part := range strings.SplitSeq(s, ",") {
		if k, v, ok := strings.Cut(part, "="); ok {
			out[k] = v
		}
	}
	return out
}
