// Package acmedns obtains a wildcard certificate for a domain over ACME
// (RFC 8555) with DNS-01 challenges (ADR 0090): one certificate for
// *.<domain> and <domain>, the base of Dock's generated hostnames, so
// those hostnames never appear in certificate-transparency logs.
//
// It is a thin layer over golang.org/x/crypto/acme: the caller brings the
// account key, the CA's directory and a DNS that publishes TXT records.
// Core runs it with a Cloudflare DNS and keeps the result sealed; nodes
// load it into Caddy.
package acmedns

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"golang.org/x/crypto/acme"
)

// LetsEncrypt is Let's Encrypt's production directory, the default CA.
const LetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

// DNS publishes the TXT records of DNS-01 challenges.
type DNS interface {
	// Present adds a TXT record at name (a fully qualified name without
	// the trailing dot, e.g. _acme-challenge.apps.example.com) with
	// value, beside any records already there.
	Present(ctx context.Context, name, value string) error
	// Wait returns once the records Present added at name are visible
	// where the CA will look for them (the zone's authoritative name
	// servers), or with an error when they don't show in time.
	Wait(ctx context.Context, name string, values []string) error
	// CleanUp removes every record Present added at name. It is called
	// once Obtain is done with them, whether it succeeded or not.
	CleanUp(ctx context.Context, name string) error
}

// Request is what Obtain needs.
type Request struct {
	// Directory is the CA's ACME directory URL; empty is LetsEncrypt.
	Directory string
	// AccountKey signs every ACME request: the account is the key, and
	// Obtain registers it (agreeing to the CA's terms) the first time.
	AccountKey crypto.Signer
	// Email is the account's contact, optional.
	Email string
	// Domain is the base domain: the certificate covers *.<Domain> and
	// <Domain>.
	Domain string
	// DNS publishes the challenge records.
	DNS DNS
	// HTTPClient talks to the CA; nil is http.DefaultClient.
	HTTPClient *http.Client
}

// Certificate is an issued certificate and its private key.
type Certificate struct {
	// ChainPEM is the leaf followed by its intermediates, PEM encoded.
	ChainPEM []byte
	// KeyPEM is the leaf's ECDSA P-256 private key, PEM encoded (PKCS #8).
	KeyPEM []byte
	// NotBefore and NotAfter are the leaf's validity.
	NotBefore, NotAfter time.Time
}

// Names are the names a certificate for the base domain covers, in the
// order they are requested.
func Names(domain string) []string { return []string{"*." + domain, domain} }

// ChallengeName is where DNS-01 looks for domain's TXT records: both
// names of the certificate are checked there.
func ChallengeName(domain string) string { return "_acme-challenge." + domain }

// NewAccountKey makes an account key: ECDSA P-256, PKCS #8 PEM.
func NewAccountKey() (crypto.Signer, []byte, error) {
	return newKey()
}

// ParseKey reads a key NewAccountKey or Obtain made.
func ParseKey(keyPEM []byte) (crypto.Signer, error) {
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		return nil, errors.New("acmedns: no PEM key")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("acmedns: parse key: %w", err)
	}
	s, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("acmedns: key can't sign")
	}
	return s, nil
}

// Obtain registers the account if it is new, orders a certificate for
// Names(r.Domain), answers its DNS-01 challenges through r.DNS and returns
// the issued chain with a fresh key. It takes as long as the CA and DNS
// take: bound it with ctx.
//
// An error from the CA is an *acme.Error or an *acme.AuthorizationError
// (wrapped); one from r.DNS is returned wrapped as it came.
func Obtain(ctx context.Context, r Request) (Certificate, error) {
	dir := r.Directory
	if dir == "" {
		dir = LetsEncrypt
	}
	cl := &acme.Client{Key: r.AccountKey, DirectoryURL: dir, HTTPClient: r.HTTPClient, UserAgent: "dock"}
	acct := &acme.Account{}
	if r.Email != "" {
		acct.Contact = []string{"mailto:" + r.Email}
	}
	if _, err := cl.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return Certificate{}, fmt.Errorf("register an ACME account: %w", err)
	}

	names := Names(r.Domain)
	order, err := cl.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return Certificate{}, fmt.Errorf("order a certificate for %s: %w", names[0], err)
	}
	if err := answer(ctx, cl, r, order.AuthzURLs); err != nil {
		return Certificate{}, err
	}
	if order, err = cl.WaitOrder(ctx, order.URI); err != nil {
		return Certificate{}, fmt.Errorf("wait for the order of %s: %w", names[0], err)
	}

	key, keyPEM, err := newKey()
	if err != nil {
		return Certificate{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: names}, key)
	if err != nil {
		return Certificate{}, fmt.Errorf("acmedns: make the CSR: %w", err)
	}
	der, _, err := cl.CreateCertFromOrder(ctx, order, csr, true)
	if err != nil {
		return Certificate{}, fmt.Errorf("finalize the certificate for %s: %w", names[0], err)
	}
	return certificate(der, keyPEM)
}

// answer fulfils the pending authorizations' DNS-01 challenges: all their
// records first (both names share one TXT name), one wait for them to be
// visible, then the challenges. Authorizations the CA already holds as
// valid for the account need nothing.
func answer(ctx context.Context, cl *acme.Client, r Request, urls []string) (err error) {
	var pending []*acme.Challenge
	var authzs []string
	var values []string
	for _, u := range urls {
		z, err := cl.GetAuthorization(ctx, u)
		if err != nil {
			return fmt.Errorf("read an authorization: %w", err)
		}
		if z.Status == acme.StatusValid {
			continue
		}
		i := slices.IndexFunc(z.Challenges, func(c *acme.Challenge) bool { return c.Type == "dns-01" })
		if i < 0 {
			return fmt.Errorf("acmedns: the CA offers no dns-01 challenge for %s", z.Identifier.Value)
		}
		v, err := cl.DNS01ChallengeRecord(z.Challenges[i].Token)
		if err != nil {
			return fmt.Errorf("acmedns: %w", err)
		}
		pending, authzs, values = append(pending, z.Challenges[i]), append(authzs, z.URI), append(values, v)
	}
	if len(pending) == 0 {
		return nil
	}

	name := ChallengeName(r.Domain)
	defer func() {
		// Clean up with a context of its own: ctx may be what ended.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if cerr := r.DNS.CleanUp(cctx, name); cerr != nil && err == nil {
			err = fmt.Errorf("remove the challenge records at %s: %w", name, cerr)
		}
	}()
	for _, v := range values {
		if err := r.DNS.Present(ctx, name, v); err != nil {
			return err
		}
	}
	if err := r.DNS.Wait(ctx, name, values); err != nil {
		return err
	}
	for i, ch := range pending {
		if _, err := cl.Accept(ctx, ch); err != nil {
			return fmt.Errorf("answer the challenge: %w", err)
		}
		if _, err := cl.WaitAuthorization(ctx, authzs[i]); err != nil {
			return fmt.Errorf("the CA didn't accept the TXT record at %s: %w", name, err)
		}
	}
	return nil
}

// newKey makes an ECDSA P-256 key, returned with its PKCS #8 PEM.
func newKey() (crypto.Signer, []byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("acmedns: make a key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, fmt.Errorf("acmedns: encode a key: %w", err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// certificate PEM-encodes an issued chain (DER, leaf first).
func certificate(der [][]byte, keyPEM []byte) (Certificate, error) {
	if len(der) == 0 {
		return Certificate{}, errors.New("acmedns: the CA returned no certificate")
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return Certificate{}, fmt.Errorf("acmedns: parse the certificate: %w", err)
	}
	var chain []byte
	for _, d := range der {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	return Certificate{ChainPEM: chain, KeyPEM: keyPEM, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}, nil
}
