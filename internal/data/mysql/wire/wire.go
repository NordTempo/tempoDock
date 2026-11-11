// Package wire is the slice of the MySQL client/server protocol the data
// panel's MySQL engine uses (ADR 0072): the handshake with optional TLS,
// the mysql_native_password and caching_sha2_password logins, COM_QUERY
// with text result sets streamed row by row, COM_PING and COM_QUIT. It
// works with MySQL 8.0+ and MariaDB 10.6+.
//
// It has no prepared statements, no binary protocol, no multi-statements
// (the server refuses "a; b"), no LOAD DATA LOCAL and no pool: a Conn is
// one connection, used by one caller at a time. Deadlines are the caller's
// (NetConn().SetDeadline); nothing here starts a goroutine or a timer.
package wire

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
)

// Capability flags (CLIENT_*) Dock sends or reads.
const (
	capLongPassword     = 1 << 0
	capFoundRows        = 1 << 1
	capLongFlag         = 1 << 2
	capConnectWithDB    = 1 << 3
	capProtocol41       = 1 << 9
	capSSL              = 1 << 11
	capTransactions     = 1 << 13
	capSecureConnection = 1 << 15
	capPluginAuth       = 1 << 19
	capConnectAttrs     = 1 << 20
	capPluginAuthLenenc = 1 << 21
)

// Status flags (SERVER_*) in OK and EOF packets.
const (
	statusInTrans         = 1 << 0
	statusMoreResults     = 1 << 3
	statusInTransReadonly = 1 << 13
)

// charsetUTF8MB4 is utf8mb4_general_ci, which MySQL and MariaDB both know.
const charsetUTF8MB4 = 45

// maxPacket is the payload size at which a packet continues in the next.
const maxPacket = 1<<24 - 1

// Caps on what a server may send, so a broken or hostile one can't make
// Dock allocate past them: a logical packet (continuations joined; a row
// of the console's DataQueryMaxBytes fits) and a result's columns
// (MySQL's own limit is 4096 a table).
const (
	maxLogicalPacket = 80 << 20
	maxColumns       = 4096
)

// Commands.
const (
	comQuit  = 0x01
	comQuery = 0x03
	comPing  = 0x0e
)

// ErrBadConn means the connection is in an unknown state (a short read, a
// protocol violation, a result abandoned mid-stream): close it.
var ErrBadConn = errors.New("mysql connection is in an unknown state")

// Error is an error packet from the server.
type Error struct {
	// Code is the server's error number, e.g. 1045 (access denied).
	Code uint16
	// State is the SQLSTATE, e.g. "28000".
	State   string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s (%d, %s)", e.Message, e.Code, e.State)
}

// Config is how to log in.
type Config struct {
	User, Password string
	// Database to use; empty for none.
	Database string
	// TLS, when set, is negotiated before the login. TLSOptional falls
	// back to plaintext when the server doesn't offer it.
	TLS         *tls.Config
	TLSOptional bool
	// Attributes are connection attributes (program_name, ...): the
	// server shows them in performance_schema.session_connect_attrs.
	Attributes map[string]string
}

// Conn is one logged-in connection. It is not safe for concurrent use.
type Conn struct {
	nc  net.Conn
	r   *bufio.Reader
	seq uint8
	// buf is reused for packets read and written.
	buf []byte
	// ID is the server's connection ID (KILL takes it).
	ID uint32
	// Version is the server's version string, e.g. "8.4.3" or
	// "11.4.4-MariaDB-ubu2404".
	Version string
	// status is the last OK or EOF packet's status flags.
	status uint16
	// broken: a call failed in a way that leaves the stream unusable.
	broken bool
	tls    bool
}

// NetConn is the underlying connection, for deadlines.
func (c *Conn) NetConn() net.Conn { return c.nc }

// Broken reports whether the connection must be closed.
func (c *Conn) Broken() bool { return c.broken }

// MarkBroken records that the connection's state is unknown (a statement
// was killed mid-flight): Close then skips COM_QUIT.
func (c *Conn) MarkBroken() { c.broken = true }

// InTransaction reports whether the server said a transaction is open.
func (c *Conn) InTransaction() bool { return c.status&statusInTrans != 0 }

// Connect runs the handshake and the login over nc, which it closes when
// they fail. The caller sets nc's deadline for them.
func Connect(nc net.Conn, cfg Config) (*Conn, error) {
	c := &Conn{nc: nc, r: bufio.NewReaderSize(nc, 16<<10)}
	if err := c.login(cfg); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return c, nil
}

// Close sends COM_QUIT (unless the connection is broken) and closes it.
func (c *Conn) Close() error {
	if !c.broken {
		c.seq = 0
		_ = c.writePacket([]byte{comQuit})
	}
	return c.nc.Close()
}

// Ping checks the connection.
func (c *Conn) Ping() error {
	c.seq = 0
	if err := c.writePacket([]byte{comPing}); err != nil {
		return err
	}
	_, err := c.readResult()
	return err
}

// Result is what a statement without a result set did.
type Result struct {
	AffectedRows uint64
	LastInsertID uint64
	Warnings     uint16
	Info         string
}

// Exec runs a statement and expects no result set (a result set is read
// and discarded).
func (c *Conn) Exec(sql string) (Result, error) {
	var res Result
	err := c.Query(sql, func([]Column) error { return nil }, func([][]byte) error { return nil }, &res)
	return res, err
}

// Column is one column of a result set.
type Column struct {
	Name string
	// Table and Schema are where it comes from, empty for expressions.
	Table, Schema string
	// Type is the protocol type (TypeLong, ...).
	Type uint8
	// Charset is the collation ID; 63 is binary.
	Charset uint16
	// Length is the display length; Flags the column flags (FlagUnsigned,
	// ...); Decimals the scale.
	Length   uint32
	Flags    uint16
	Decimals uint8
}

// Column flags.
const (
	FlagNotNull  = 1 << 0
	FlagUnsigned = 1 << 5
	FlagBinary   = 1 << 7
)

// CharsetBinary marks binary strings (BLOB, BINARY, VARBINARY).
const CharsetBinary = 63

// Protocol types (MYSQL_TYPE_*).
const (
	TypeDecimal    = 0
	TypeTiny       = 1
	TypeShort      = 2
	TypeLong       = 3
	TypeFloat      = 4
	TypeDouble     = 5
	TypeNull       = 6
	TypeTimestamp  = 7
	TypeLongLong   = 8
	TypeInt24      = 9
	TypeDate       = 10
	TypeTime       = 11
	TypeDatetime   = 12
	TypeYear       = 13
	TypeNewDate    = 14
	TypeVarchar    = 15
	TypeBit        = 16
	TypeJSON       = 245
	TypeNewDecimal = 246
	TypeEnum       = 247
	TypeSet        = 248
	TypeTinyBlob   = 249
	TypeMediumBlob = 250
	TypeLongBlob   = 251
	TypeBlob       = 252
	TypeVarString  = 253
	TypeString     = 254
	TypeGeometry   = 255
)

// Query runs sql with COM_QUERY. A statement without a result set fills
// res (when not nil) and calls neither callback. One with a result set
// calls columns once, then row per row with the row's values in the text
// protocol (nil for NULL). The values point into a buffer reused by the
// next row: copy what must outlive the call. An error from a callback
// stops reading; the connection is then Broken, since the rest of the
// result is still on the wire. Server errors are *Error and leave the
// connection usable.
func (c *Conn) Query(sql string, columns func([]Column) error, row func([][]byte) error, res *Result) error {
	if c.broken {
		return ErrBadConn
	}
	c.seq = 0
	if err := c.writeCommand(comQuery, sql); err != nil {
		return err
	}
	pkt, err := c.readPacket()
	if err != nil {
		return err
	}
	switch {
	case len(pkt) > 0 && pkt[0] == 0x00:
		r, err := c.parseOK(pkt)
		if err == nil && res != nil {
			*res = r
		}
		return c.noMoreResults(err)
	case len(pkt) > 0 && pkt[0] == 0xff:
		return c.parseErr(pkt)
	case len(pkt) > 0 && pkt[0] == 0xfb:
		// LOCAL INFILE: never asked for (no CLIENT_LOCAL_FILES).
		return c.fail(errors.New("the server asked for a local file"))
	}
	n, _, ok := lenencInt(pkt)
	if !ok || n == 0 || n > maxColumns {
		return c.fail(errors.New("bad column count"))
	}
	cols := make([]Column, n)
	for i := range cols {
		pkt, err := c.readPacket()
		if err != nil {
			return err
		}
		if cols[i], err = parseColumn(pkt); err != nil {
			return c.fail(err)
		}
	}
	if err := c.readEOF(); err != nil {
		return err
	}
	if err := columns(cols); err != nil {
		c.broken = true
		return err
	}
	vals := make([][]byte, n)
	for {
		pkt, err := c.readPacket()
		if err != nil {
			return err
		}
		if len(pkt) > 0 && pkt[0] == 0xff {
			return c.parseErr(pkt)
		}
		if len(pkt) > 0 && pkt[0] == 0xfe && len(pkt) < 9 {
			c.parseEOF(pkt)
			return c.noMoreResults(nil)
		}
		if err := parseRow(pkt, vals); err != nil {
			return c.fail(err)
		}
		if err := row(vals); err != nil {
			c.broken = true
			return err
		}
	}
}

// noMoreResults fails on a second result set, which needs CLIENT_MULTI_*
// flags Dock never sends.
func (c *Conn) noMoreResults(err error) error {
	if err == nil && c.status&statusMoreResults != 0 {
		return c.fail(errors.New("unexpected second result set"))
	}
	return err
}

// readEOF reads the EOF packet after column definitions.
func (c *Conn) readEOF() error {
	pkt, err := c.readPacket()
	if err != nil {
		return err
	}
	if len(pkt) > 0 && pkt[0] == 0xff {
		return c.parseErr(pkt)
	}
	if len(pkt) == 0 || pkt[0] != 0xfe || len(pkt) >= 9 {
		return c.fail(errors.New("expected EOF after the columns"))
	}
	c.parseEOF(pkt)
	return nil
}

// fail marks the connection broken and wraps err with ErrBadConn.
func (c *Conn) fail(err error) error {
	c.broken = true
	return fmt.Errorf("%w: %w", ErrBadConn, err)
}

// readResult reads an OK or an error packet.
func (c *Conn) readResult() (Result, error) {
	pkt, err := c.readPacket()
	if err != nil {
		return Result{}, err
	}
	switch {
	case len(pkt) > 0 && pkt[0] == 0x00:
		return c.parseOK(pkt)
	case len(pkt) > 0 && pkt[0] == 0xff:
		return Result{}, c.parseErr(pkt)
	}
	return Result{}, c.fail(fmt.Errorf("unexpected packet 0x%02x", firstByte(pkt)))
}

func firstByte(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

// parseOK reads an OK packet (header 0x00 or 0xfe).
func (c *Conn) parseOK(pkt []byte) (Result, error) {
	var r Result
	p := pkt[1:]
	var ok bool
	var n int
	if r.AffectedRows, n, ok = lenencInt(p); !ok {
		return r, c.fail(errors.New("bad OK packet"))
	}
	p = p[n:]
	if r.LastInsertID, n, ok = lenencInt(p); !ok || len(p) < n+4 {
		return r, c.fail(errors.New("bad OK packet"))
	}
	p = p[n:]
	c.status = binary.LittleEndian.Uint16(p)
	r.Warnings = binary.LittleEndian.Uint16(p[2:])
	r.Info = string(p[4:])
	return r, nil
}

// parseEOF reads an EOF packet's status flags.
func (c *Conn) parseEOF(pkt []byte) {
	if len(pkt) >= 5 {
		c.status = binary.LittleEndian.Uint16(pkt[3:])
	}
}

// parseErr turns an error packet into *Error.
func (c *Conn) parseErr(pkt []byte) error {
	if len(pkt) < 3 {
		return c.fail(errors.New("short error packet"))
	}
	e := &Error{Code: binary.LittleEndian.Uint16(pkt[1:])}
	msg := pkt[3:]
	if len(msg) >= 6 && msg[0] == '#' {
		e.State, msg = string(msg[1:6]), msg[6:]
	}
	e.Message = string(msg)
	return e
}

// parseColumn reads a ColumnDefinition41 packet.
func parseColumn(pkt []byte) (Column, error) {
	var col Column
	var fields [6]string
	p := pkt
	for i := range fields {
		s, n, ok := lenencString(p)
		if !ok {
			return col, errors.New("bad column definition")
		}
		fields[i], p = string(s), p[n:]
	}
	col.Schema, col.Table, col.Name = fields[1], fields[2], fields[4]
	// Fixed-length fields: their length (0x0c), charset, length, type,
	// flags, decimals.
	if len(p) < 13 {
		return col, errors.New("short column definition")
	}
	col.Charset = binary.LittleEndian.Uint16(p[1:])
	col.Length = binary.LittleEndian.Uint32(p[3:])
	col.Type = p[7]
	col.Flags = binary.LittleEndian.Uint16(p[8:])
	col.Decimals = p[10]
	return col, nil
}

// parseRow splits a text protocol row into vals (nil for NULL), pointing
// into pkt.
func parseRow(pkt []byte, vals [][]byte) error {
	p := pkt
	for i := range vals {
		if len(p) > 0 && p[0] == 0xfb {
			vals[i], p = nil, p[1:]
			continue
		}
		s, n, ok := lenencString(p)
		if !ok {
			return errors.New("bad row")
		}
		vals[i], p = s[:len(s):len(s)], p[n:]
	}
	if len(p) != 0 {
		return errors.New("row longer than its columns")
	}
	return nil
}

// lenencInt reads a length-encoded integer: the value and how many bytes
// it took.
func lenencInt(b []byte) (uint64, int, bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	switch b[0] {
	case 0xfc:
		if len(b) < 3 {
			return 0, 0, false
		}
		return uint64(binary.LittleEndian.Uint16(b[1:])), 3, true
	case 0xfd:
		if len(b) < 4 {
			return 0, 0, false
		}
		return uint64(b[1]) | uint64(b[2])<<8 | uint64(b[3])<<16, 4, true
	case 0xfe:
		if len(b) < 9 {
			return 0, 0, false
		}
		return binary.LittleEndian.Uint64(b[1:]), 9, true
	case 0xfb, 0xff:
		return 0, 0, false
	}
	return uint64(b[0]), 1, true
}

// lenencString reads a length-encoded string and how many bytes it took.
func lenencString(b []byte) ([]byte, int, bool) {
	n, m, ok := lenencInt(b)
	if !ok || uint64(len(b)-m) < n {
		return nil, 0, false
	}
	return b[m : m+int(n)], m + int(n), true
}

// appendLenencInt appends n length-encoded.
func appendLenencInt(b []byte, n uint64) []byte {
	switch {
	case n < 251:
		return append(b, byte(n))
	case n < 1<<16:
		return append(b, 0xfc, byte(n), byte(n>>8))
	case n < 1<<24:
		return append(b, 0xfd, byte(n), byte(n>>8), byte(n>>16))
	}
	b = append(b, 0xfe)
	return binary.LittleEndian.AppendUint64(b, n)
}

// appendLenencString appends s length-encoded.
func appendLenencString(b []byte, s string) []byte {
	return append(appendLenencInt(b, uint64(len(s))), s...)
}

// readPacket reads one logical packet (joining continuations, at most
// maxLogicalPacket) into c.buf. The result is valid until the next read.
func (c *Conn) readPacket() ([]byte, error) {
	c.buf = c.buf[:0]
	for {
		var h [4]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return nil, c.fail(err)
		}
		n := int(uint32(h[0]) | uint32(h[1])<<8 | uint32(h[2])<<16)
		if h[3] != c.seq {
			return nil, c.fail(fmt.Errorf("packet out of order (%d, want %d)", h[3], c.seq))
		}
		c.seq++
		start := len(c.buf)
		if start+n > maxLogicalPacket {
			return nil, c.fail(fmt.Errorf("the server sent a row or reply over %d MB; select fewer or smaller columns", maxLogicalPacket>>20))
		}
		c.buf = slices.Grow(c.buf, n)[:start+n]
		if _, err := io.ReadFull(c.r, c.buf[start:]); err != nil {
			return nil, c.fail(err)
		}
		if n < maxPacket {
			return c.buf, nil
		}
	}
}

// writeCommand writes a command byte and its argument.
func (c *Conn) writeCommand(cmd byte, arg string) error {
	b := append(c.buf[:0], cmd)
	b = append(b, arg...)
	c.buf = b
	return c.writePacket(b)
}

// writePacket writes payload, split at maxPacket.
func (c *Conn) writePacket(payload []byte) error {
	for {
		n := min(len(payload), maxPacket)
		h := [4]byte{byte(n), byte(n >> 8), byte(n >> 16), c.seq}
		c.seq++
		bufs := net.Buffers{h[:], payload[:n]}
		if _, err := bufs.WriteTo(c.nc); err != nil {
			return c.fail(err)
		}
		payload = payload[n:]
		if n < maxPacket {
			return nil
		}
	}
}

// ErrorCode is the server error number of err, 0 when it isn't one.
func ErrorCode(err error) uint16 {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return 0
}
