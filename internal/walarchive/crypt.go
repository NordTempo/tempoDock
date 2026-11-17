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
	"strconv"
)

// Each object is gzip, then AES-256-GCM in chunks of chunkSize bytes
// (the STREAM construction), under a key of its own:
//
//	header = magic || salt (16 random bytes)
//	key    = HKDF-SHA256(database key, salt, "dock-wal object " || file)
//	chunk i is sealed under nonce 0 (7 bytes) || i (4 bytes) || last (1 byte)
//	with header || file as additional data
//
// The database key is the archive key with the service's ID in its
// derivation (ServiceKey), so no two databases share a key, and no two
// objects do either: a fresh salt per object makes counter nonces safe
// however many objects there are. A truncated object, chunks in another
// order, a changed header or salt, or an object renamed to another file
// or moved to another database fail to open.

// magic starts every object.
const magic = "DWAL2"

// saltSize is the random salt in each object's header.
const saltSize = 16

// headerSize is magic and salt.
const headerSize = len(magic) + saltSize

// chunkSize is how much plaintext one sealed chunk holds.
const chunkSize = 64 << 10

// KeySize is the size of the archive key and of the keys derived from
// it.
const KeySize = 32

// DeriveKey is the archive key from the backup repository password: one
// password opens everything a database's backups hold, restic's and the
// WAL archive. Archivers and jobs get a database's key (ServiceKey), not
// this one or the password.
func DeriveKey(password string) ([]byte, error) {
	return hkdf.Key(sha256.New, []byte(password), nil, "dock-wal 2", KeySize)
}

// ServiceKey is one database's key, derived from the archive key with
// its service ID: an archiver holding it can't open another database's
// archive.
func ServiceKey(archiveKey []byte, serviceID int64) ([]byte, error) {
	if len(archiveKey) != KeySize {
		return nil, fmt.Errorf("the WAL archive key must be %d bytes", KeySize)
	}
	return hkdf.Key(sha256.New, archiveKey, nil, "dock-wal service "+strconv.FormatInt(serviceID, 10), KeySize)
}

// objectKey is one object's key, from the database key, the object's
// salt and its file name.
func objectKey(key, salt []byte, file string) ([]byte, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("the WAL archive key must be %d bytes", KeySize)
	}
	return hkdf.Key(sha256.New, key, salt, "dock-wal object "+file, KeySize)
}

// Seal writes file's content from r to w, compressed and encrypted
// under a key of its own derived from key (a database's, ServiceKey).
func Seal(w io.Writer, r io.Reader, key []byte, file string) error {
	header := make([]byte, headerSize)
	copy(header, magic)
	if _, err := rand.Read(header[len(magic):]); err != nil {
		return err
	}
	aead, err := objectAEAD(key, header, file)
	if err != nil {
		return err
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	cw := &chunkWriter{w: w, aead: aead, ad: additional(header, file), buf: make([]byte, 0, chunkSize)}
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

// Open returns a reader of file's content from an object read from r,
// sealed under key (the database's).
func Open(r io.Reader, key []byte, file string) (io.Reader, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	if string(header[:len(magic)]) != magic {
		return nil, fmt.Errorf("%s is not a WAL archive object", file)
	}
	aead, err := objectAEAD(key, header, file)
	if err != nil {
		return nil, err
	}
	cr := &chunkReader{r: bufio.NewReaderSize(r, chunkSize+aead.Overhead()+1), aead: aead, ad: additional(header, file)}
	return gzip.NewReader(cr)
}

// objectAEAD is the cipher of the object whose header is header.
func objectAEAD(key, header []byte, file string) (cipher.AEAD, error) {
	k, err := objectKey(key, header[len(magic):], file)
	if err != nil {
		return nil, err
	}
	return newAEAD(k)
}

// additional is every chunk's additional data: the header and the file.
func additional(header []byte, file string) []byte {
	return append(append([]byte{}, header...), file...)
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

// nonce is chunk n's nonce: the object's key is its own, so a counter
// is enough.
func nonce(n uint32, last bool) []byte {
	out := make([]byte, 12)
	binary.BigEndian.PutUint32(out[7:], n)
	if last {
		out[11] = 1
	}
	return out
}

// chunkWriter seals what it is given in chunks.
type chunkWriter struct {
	w    io.Writer
	aead cipher.AEAD
	ad   []byte
	buf  []byte
	n    uint32
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
	sealed := c.aead.Seal(nil, nonce(c.n, last), c.buf, c.ad)
	c.n++
	c.buf = c.buf[:0]
	_, err := c.w.Write(sealed)
	return err
}

func (c *chunkWriter) close() error { return c.flush(true) }

// chunkReader opens sealed chunks in order.
type chunkReader struct {
	r     *bufio.Reader
	aead  cipher.AEAD
	ad    []byte
	n     uint32
	plain []byte
	done  bool
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
	plain, err := c.aead.Open(nil, nonce(c.n, last), sealed[:k], c.ad)
	if err != nil {
		return errors.New("the WAL archive object doesn't open: wrong key, or damaged")
	}
	c.n++
	c.plain, c.done = plain, last
	return nil
}
