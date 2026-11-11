package wire

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// Auth plugins Dock can log in with. sha256_password is also what MySQL
// switches a login for an unknown user to, before refusing it.
const (
	pluginNative = "mysql_native_password"
	pluginSHA2   = "caching_sha2_password"
	pluginSHA256 = "sha256_password"
)

// handshake is the server's initial packet, as far as the login needs it.
type handshake struct {
	caps    uint32
	version string
	id      uint32
	// seed is the 20-byte scramble the password is hashed with.
	seed   []byte
	plugin string
}

// parseHandshake reads a protocol 10 handshake.
func parseHandshake(pkt []byte) (handshake, error) {
	var hs handshake
	bad := errors.New("bad handshake from the server")
	if len(pkt) < 1 || pkt[0] != 10 {
		return hs, fmt.Errorf("unsupported protocol version %d", firstByte(pkt))
	}
	p := pkt[1:]
	end := bytes.IndexByte(p, 0)
	if end < 0 {
		return hs, bad
	}
	hs.version, p = string(p[:end]), p[end+1:]
	if len(p) < 4+8+1+2 {
		return hs, bad
	}
	hs.id = binary.LittleEndian.Uint32(p)
	hs.seed = append([]byte(nil), p[4:12]...)
	hs.caps = uint32(binary.LittleEndian.Uint16(p[13:]))
	p = p[15:]
	if len(p) < 1+2+2+1+10 {
		return hs, bad
	}
	hs.caps |= uint32(binary.LittleEndian.Uint16(p[3:])) << 16
	authLen := int(p[5])
	p = p[16:]
	if hs.caps&capSecureConnection != 0 {
		n := max(13, authLen-8)
		if len(p) < n {
			return hs, bad
		}
		// 12 more bytes of scramble, then a NUL.
		hs.seed = append(hs.seed, bytes.TrimRight(p[:n], "\x00")...)
		p = p[n:]
	}
	if hs.caps&capPluginAuth != 0 {
		if end := bytes.IndexByte(p, 0); end >= 0 {
			p = p[:end]
		}
		hs.plugin = string(p)
	}
	return hs, nil
}

// login reads the handshake, negotiates TLS and authenticates.
func (c *Conn) login(cfg Config) error {
	c.seq = 0
	pkt, err := c.readPacket()
	if err != nil {
		return err
	}
	if len(pkt) > 0 && pkt[0] == 0xff {
		// Refused before the handshake: too many connections, host blocked.
		return c.parseErr(pkt)
	}
	hs, err := parseHandshake(pkt)
	if err != nil {
		return c.fail(err)
	}
	c.ID, c.Version = hs.id, hs.version
	const need = capProtocol41 | capSecureConnection | capPluginAuth
	if hs.caps&need != need {
		return c.fail(errors.New("the server is too old (no protocol 4.1 logins)"))
	}
	caps := uint32(capLongPassword | capFoundRows | capLongFlag | capProtocol41 | capTransactions |
		capSecureConnection | capPluginAuth | capPluginAuthLenenc)
	if cfg.Database != "" {
		caps |= capConnectWithDB
	}
	if len(cfg.Attributes) > 0 {
		caps |= capConnectAttrs
	}
	caps &= hs.caps
	if cfg.TLS != nil {
		switch {
		case hs.caps&capSSL != 0:
			caps |= capSSL
			if err := c.startTLS(caps, cfg.TLS); err != nil {
				return err
			}
		case !cfg.TLSOptional:
			return c.fail(errors.New("the server doesn't support TLS"))
		}
	}

	plugin := cmp.Or(hs.plugin, pluginNative)
	auth, err := c.answer(plugin, cfg.Password, hs.seed)
	if err != nil {
		// Unknown default plugin: answer as native; the server switches us
		// to the user's plugin if it is one Dock knows.
		plugin = pluginNative
		auth, _ = c.answer(plugin, cfg.Password, hs.seed)
	}
	b := binary.LittleEndian.AppendUint32(nil, caps)
	b = binary.LittleEndian.AppendUint32(b, maxPacket)
	b = append(b, charsetUTF8MB4)
	b = append(b, make([]byte, 23)...)
	b = append(append(b, cfg.User...), 0)
	b = appendLenencString(b, string(auth))
	if caps&capConnectWithDB != 0 {
		b = append(append(b, cfg.Database...), 0)
	}
	b = append(append(b, plugin...), 0)
	if caps&capConnectAttrs != 0 {
		var attrs []byte
		for _, k := range slices.Sorted(maps.Keys(cfg.Attributes)) {
			attrs = appendLenencString(appendLenencString(attrs, k), cfg.Attributes[k])
		}
		b = appendLenencString(b, string(attrs))
	}
	if err := c.writePacket(b); err != nil {
		return err
	}
	return c.authResult(cfg.Password, plugin, hs.seed)
}

// startTLS sends an SSL request and upgrades the connection.
func (c *Conn) startTLS(caps uint32, cfg *tls.Config) error {
	b := binary.LittleEndian.AppendUint32(nil, caps)
	b = binary.LittleEndian.AppendUint32(b, maxPacket)
	b = append(b, charsetUTF8MB4)
	b = append(b, make([]byte, 23)...)
	if err := c.writePacket(b); err != nil {
		return err
	}
	tc := tls.Client(c.nc, cfg)
	if err := tc.Handshake(); err != nil {
		return c.fail(fmt.Errorf("TLS: %w", err))
	}
	c.nc, c.tls = tc, true
	c.r.Reset(tc)
	return nil
}

// authResult reads the server's answers to a login until OK or an error,
// following auth switches and caching_sha2_password's extra rounds.
func (c *Conn) authResult(password, plugin string, seed []byte) error {
	for {
		pkt, err := c.readPacket()
		if err != nil {
			return err
		}
		switch firstByte(pkt) {
		case 0x00:
			_, err := c.parseOK(pkt)
			return err
		case 0xff:
			return c.parseErr(pkt)
		case 0xfe:
			// Auth switch: another plugin, with its own seed.
			p := pkt[1:]
			end := bytes.IndexByte(p, 0)
			if end < 0 {
				return c.fail(errors.New("bad auth switch"))
			}
			plugin, seed = string(p[:end]), bytes.TrimRight(p[end+1:], "\x00")
			auth, err := c.answer(plugin, password, seed)
			if err != nil {
				return c.fail(err)
			}
			if err := c.writePacket(auth); err != nil {
				return err
			}
		case 0x01:
			if plugin == pluginSHA256 {
				// The RSA key Dock asked for.
				if err := c.sendRSA(password, seed, pkt[1:]); err != nil {
					return err
				}
				continue
			}
			if plugin != pluginSHA2 || len(pkt) < 2 {
				return c.fail(errors.New("unexpected auth data"))
			}
			switch pkt[1] {
			case 3: // fast auth succeeded; the OK follows
			case 4: // full auth: the password itself, over TLS or RSA
				if err := c.fullAuth(password, seed); err != nil {
					return err
				}
			default:
				return c.fail(errors.New("unexpected auth data"))
			}
		default:
			return c.fail(fmt.Errorf("unexpected packet 0x%02x during login", firstByte(pkt)))
		}
	}
}

// fullAuth sends the password for caching_sha2_password's full auth: in
// clear over TLS, else encrypted with the server's RSA key (asked for on
// the connection; Dock's connections run on private networks).
func (c *Conn) fullAuth(password string, seed []byte) error {
	plain := append([]byte(password), 0)
	if c.tls {
		return c.writePacket(plain)
	}
	if err := c.writePacket([]byte{2}); err != nil {
		return err
	}
	pkt, err := c.readPacket()
	if err != nil {
		return err
	}
	if firstByte(pkt) != 0x01 {
		return c.fail(errors.New("the server sent no public key"))
	}
	return c.sendRSA(password, seed, pkt[1:])
}

// sendRSA sends the password, XORed with the seed, encrypted with the
// server's RSA key (PEM).
func (c *Conn) sendRSA(password string, seed, keyPEM []byte) error {
	plain := append([]byte(password), 0)
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return c.fail(errors.New("bad public key from the server"))
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return c.fail(fmt.Errorf("public key: %w", err))
	}
	pub, ok := key.(*rsa.PublicKey)
	if !ok {
		return c.fail(errors.New("the server's public key isn't RSA"))
	}
	if len(seed) == 0 {
		return c.fail(errors.New("the server sent no seed to encrypt the password with"))
	}
	for i := range plain {
		plain[i] ^= seed[i%len(seed)]
	}
	// OAEP with SHA-1 is what the server decrypts with.
	enc, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, plain, nil)
	if err != nil {
		return c.fail(err)
	}
	return c.writePacket(enc)
}

// answer is the login answer for plugin: scramble's, except that
// sha256_password over TLS takes the password itself.
func (c *Conn) answer(plugin, password string, seed []byte) ([]byte, error) {
	if plugin == pluginSHA256 && c.tls {
		return append([]byte(password), 0), nil
	}
	return scramble(plugin, password, seed)
}

// scramble is the login answer for plugin: the password hashed with the
// server's seed (or, for sha256_password, a request for the server's RSA
// key), never the password itself.
func scramble(plugin, password string, seed []byte) ([]byte, error) {
	if len(seed) > 20 {
		seed = seed[:20]
	}
	switch plugin {
	case pluginNative:
		if password == "" {
			return nil, nil
		}
		// SHA1(password) XOR SHA1(seed + SHA1(SHA1(password))): the
		// protocol defines it over SHA-1.
		h1 := sha1.Sum([]byte(password))
		h2 := sha1.Sum(h1[:])
		h := sha1.New()
		h.Write(seed)
		h.Write(h2[:])
		out := h.Sum(nil)
		for i := range out {
			out[i] ^= h1[i]
		}
		return out, nil
	case pluginSHA2:
		if password == "" {
			return nil, nil
		}
		// SHA256(password) XOR SHA256(SHA256(SHA256(password)) + seed)
		m1 := sha256.Sum256([]byte(password))
		m2 := sha256.Sum256(m1[:])
		h := sha256.New()
		h.Write(m2[:])
		h.Write(seed)
		out := h.Sum(nil)
		for i := range out {
			out[i] ^= m1[i]
		}
		return out, nil
	case pluginSHA256:
		// Not a hash: ask for the server's RSA key (authResult sends the
		// password encrypted with it; over TLS answer sends the password).
		if password == "" {
			return []byte{0}, nil
		}
		return []byte{1}, nil
	}
	return nil, fmt.Errorf("the user logs in with %s, which Dock doesn't support; use caching_sha2_password or mysql_native_password", plugin)
}
