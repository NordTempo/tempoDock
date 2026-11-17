package walarchive

import (
	"bufio"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Each object is gzip, then AES-256-GCM in chunks of chunkSize bytes
// (the STREAM construction): after a magic and a random 7-byte prefix,
// chunk i is sealed under nonce prefix || i (4 bytes) || last (1 byte),
// with the file's name as additional data. A truncated object, chunks
// in another order, or an object renamed to another file fail to open.

// magic starts every object.
const magic = "DWAL1"

// chunkSize is how much plaintext one sealed chunk holds.
const chunkSize = 64 << 10

// prefixSize is the random part of the nonces.
const prefixSize = 7

// KeySize is the archive key's size.
const KeySize = 32

// DeriveKey is the archive key from the backup repository password: one
// password opens everything a database's backups hold, restic's and the
// WAL archive, and the archiver gets only this key, not the password.
func DeriveKey(password string) ([]byte, error) {
	return hkdf.Key(sha256.New, []byte(password), nil, "dock-wal 1", KeySize)
}

// Seal writes file's content from r to w, compressed and encrypted
// under key.
func Seal(w io.Writer, r io.Reader, key []byte, file string) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	prefix := make([]byte, prefixSize)
	if _, err := rand.Read(prefix); err != nil {
		return err
	}
	if _, err := io.WriteString(w, magic); err != nil {
		return err
	}
	if _, err := w.Write(prefix); err != nil {
		return err
	}
	cw := &chunkWriter{w: w, aead: aead, prefix: prefix, ad: []byte(file), buf: make([]byte, 0, chunkSize)}
	zw, err := gzip.NewWriterLevel(cw, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if _, err := io.Copy(zw, r); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return cw.close()
}

// Open returns a reader of file's content from an object read from r.
func Open(r io.Reader, key []byte, file string) (io.Reader, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	head := make([]byte, len(magic)+prefixSize)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	if string(head[:len(magic)]) != magic {
		return nil, fmt.Errorf("%s is not a WAL archive object", file)
	}
	cr := &chunkReader{r: bufio.NewReaderSize(r, chunkSize+aead.Overhead()+1), aead: aead, prefix: head[len(magic):], ad: []byte(file)}
	return gzip.NewReader(cr)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("the WAL archive key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// nonce is chunk n's nonce.
func nonce(prefix []byte, n uint32, last bool) []byte {
	out := make([]byte, 12)
	copy(out, prefix)
	binary.BigEndian.PutUint32(out[prefixSize:], n)
	if last {
		out[11] = 1
	}
	return out
}

// chunkWriter seals what it is given in chunks.
type chunkWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	prefix []byte
	ad     []byte
	buf    []byte
	n      uint32
}

func (c *chunkWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if len(c.buf) == chunkSize {
			// Full and more follows: not the last chunk.
			if err := c.flush(false); err != nil {
				return written, err
			}
		}
		k := copy(c.buf[len(c.buf):chunkSize], p)
		c.buf = c.buf[:len(c.buf)+k]
		p, written = p[k:], written+k
	}
	return written, nil
}

func (c *chunkWriter) flush(last bool) error {
	if c.n == ^uint32(0) {
		return errors.New("WAL archive object too large")
	}
	sealed := c.aead.Seal(nil, nonce(c.prefix, c.n, last), c.buf, c.ad)
	c.n++
	c.buf = c.buf[:0]
	_, err := c.w.Write(sealed)
	return err
}

func (c *chunkWriter) close() error { return c.flush(true) }

// chunkReader opens sealed chunks in order.
type chunkReader struct {
	r      *bufio.Reader
	aead   cipher.AEAD
	prefix []byte
	ad     []byte
	n      uint32
	plain  []byte
	done   bool
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.plain) == 0 {
		if c.done {
			return 0, io.EOF
		}
		if err := c.next(); err != nil {
			return 0, err
		}
	}
	k := copy(p, c.plain)
	c.plain = c.plain[k:]
	return k, nil
}

// next opens the next chunk: a full one followed by more data, or the
// last (which may be full too).
func (c *chunkReader) next() error {
	sealed := make([]byte, chunkSize+c.aead.Overhead())
	k, err := io.ReadFull(c.r, sealed)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		if errors.Is(err, io.EOF) {
			return errors.New("the WAL archive object is cut short")
		}
		return err
	}
	last := k < len(sealed)
	if !last {
		if _, err := c.r.Peek(1); errors.Is(err, io.EOF) {
			last = true
		}
	}
	plain, err := c.aead.Open(nil, nonce(c.prefix, c.n, last), sealed[:k], c.ad)
	if err != nil {
		return errors.New("the WAL archive object doesn't open: wrong key, or damaged")
	}
	c.n++
	c.plain, c.done = plain, last
	return nil
}
