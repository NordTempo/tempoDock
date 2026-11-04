// Package runframe is the framing of a one-off command's streams (`dock
// run`, ADR 0048): its input, terminal size, output and end travel as
// frames over one connection, both between the CLI and the control plane
// (an HTTP/1.1 upgrade to apitypes.RunProtocol) and between the control
// plane and a remote server (the node link's run-job-stream call).
//
// A frame is one type byte, a big-endian uint32 payload length and the
// payload:
//
//	caller → command: Stdin (bytes), CloseStdin (empty), Resize (cols, rows as uint16)
//	command → caller: Stdout, Stderr (bytes), then exactly one Exit (JSON)
//
// It is the standard library only: the client-only CLI build imports it.
package runframe

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Type says what a frame carries.
type Type byte

// Frame types.
const (
	Stdin      Type = 'i'
	CloseStdin Type = 'c'
	Resize     Type = 'r'
	Stdout     Type = 'o'
	Stderr     Type = 'e'
	Exit       Type = 'x'
)

// MaxPayload caps one frame's payload. Writers split bigger writes; a
// reader refuses a larger frame instead of buffering it.
const MaxPayload = 64 << 10

const headerSize = 5

// ErrTooLarge is a frame over MaxPayload.
var ErrTooLarge = fmt.Errorf("run frame over the %d byte limit", MaxPayload)

// Writer writes frames. It is safe for concurrent use: frames from
// several goroutines (output and the end) never interleave.
type Writer struct {
	mu  sync.Mutex
	w   io.Writer
	buf []byte
}

// NewWriter returns a Writer on w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Frame writes one frame of type t, splitting p into frames of at most
// MaxPayload bytes. An empty p writes one empty frame.
func (fw *Writer) Frame(t Type, p []byte) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	for first := true; first || len(p) > 0; first = false {
		n := min(len(p), MaxPayload)
		fw.buf = append(fw.buf[:0], byte(t), 0, 0, 0, 0)
		binary.BigEndian.PutUint32(fw.buf[1:headerSize], uint32(n))
		fw.buf = append(fw.buf, p[:n]...)
		if _, err := fw.w.Write(fw.buf); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// Resize writes a Resize frame.
func (fw *Writer) Resize(cols, rows uint16) error {
	var p [4]byte
	binary.BigEndian.PutUint16(p[0:2], cols)
	binary.BigEndian.PutUint16(p[2:4], rows)
	return fw.Frame(Resize, p[:])
}

// Stream is an io.Writer that writes every Write as frames of type t,
// e.g. a command's stdout.
func (fw *Writer) Stream(t Type) io.Writer { return streamWriter{fw, t} }

type streamWriter struct {
	fw *Writer
	t  Type
}

func (s streamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := s.fw.Frame(s.t, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Reader reads frames.
type Reader struct {
	br  *bufio.Reader
	buf []byte
}

// NewReader returns a Reader on r.
func NewReader(r io.Reader) *Reader { return &Reader{br: bufio.NewReaderSize(r, 16<<10)} }

// Next returns the next frame. The payload is only valid until the next
// call. A stream that ends cleanly between frames is io.EOF; one that ends
// inside a frame is io.ErrUnexpectedEOF.
func (fr *Reader) Next() (Type, []byte, error) {
	var h [headerSize]byte
	if _, err := io.ReadFull(fr.br, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > MaxPayload {
		return 0, nil, ErrTooLarge
	}
	if cap(fr.buf) < int(n) {
		fr.buf = make([]byte, n)
	}
	fr.buf = fr.buf[:n]
	if _, err := io.ReadFull(fr.br, fr.buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return Type(h[0]), fr.buf, nil
}

// ParseResize reads a Resize frame's payload.
func ParseResize(p []byte) (cols, rows uint16, err error) {
	if len(p) != 4 {
		return 0, 0, errors.New("resize frame: want 4 bytes")
	}
	return binary.BigEndian.Uint16(p[0:2]), binary.BigEndian.Uint16(p[2:4]), nil
}
