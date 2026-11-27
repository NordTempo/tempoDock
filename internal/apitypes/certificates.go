package apitypes

import "time"

// Certificate is a TLS certificate an org uploaded (ADR 0112), for names
// ACME can't cover: a Cloudflare origin certificate, an internal or
// corporate CA. It serves every verified domain of the org its names
// cover, instead of a certificate from Let's Encrypt, until it expires.
// It doesn't renew. Its private key is never returned.
type Certificate struct {
	ID int64 `json:"id"`
	// Names are the certificate's DNS names, like app.example.com or
	// *.corp.example.com (one label under it).
	Names  []string `json:"names"`
	Issuer string   `json:"issuer" example:"Cloudflare Origin SSL ECC Certificate Authority"`
	// Public says it chained to a CA in the control plane's system trust
	// store when it was uploaded: usually what browsers trust too, but a
	// CA added to that store counts, and a browser may not have it. A
	// Cloudflare origin certificate or an internal CA's usually isn't,
	// unless that store holds its CA; either is fine where only Cloudflare
	// or your own machines connect.
	Public bool `json:"public"`
	// Fingerprint is the SHA-256 of the leaf certificate, in hex.
	Fingerprint string    `json:"fingerprint"`
	NotBefore   time.Time `json:"not_before"`
	// NotAfter is when the chain stops working: the leaf's end, or an
	// intermediate's if that comes first. Serving and alerts go by it.
	NotAfter time.Time         `json:"not_after"`
	Status   CertificateStatus `json:"status" enum:"active,expiring,expired,unused"`
	// Domains are the org's domains it serves now. A domain covered by
	// several certificates gets the one that expires last.
	Domains   []CertificateDomain `json:"domains"`
	CreatedBy string              `json:"created_by"`
	CreatedAt time.Time           `json:"created_at"`
}

// CertificateStatus is where an uploaded certificate stands.
type CertificateStatus string

// Certificate statuses.
const (
	// CertificateActive: valid for more than 30 days.
	CertificateActive CertificateStatus = "active"
	// CertificateExpiring: valid for 30 days or less; Dock has alerted.
	CertificateExpiring CertificateStatus = "expiring"
	// CertificateExpired: no longer served; its domains get certificates
	// from Let's Encrypt again where it can issue them.
	CertificateExpired CertificateStatus = "expired"
	// CertificateUnused: valid, but no domain uses it: none of the org's
	// domains matches, or a certificate that expires later covers them.
	CertificateUnused CertificateStatus = "unused"
)

// CertificateDomain is a domain an uploaded certificate serves, and where
// it is.
type CertificateDomain struct {
	Hostname    string `json:"hostname"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
}

// AddCertificateRequest uploads a certificate: its chain and private key
// in PEM.
type AddCertificateRequest struct {
	// Chain is the leaf certificate and its intermediates, in PEM. Dock
	// puts them in order, leaf first.
	Chain string `json:"chain" maxLength:"65536"`
	// Key is the private key, in PEM (PKCS#8, PKCS#1 or EC). It is sealed
	// at rest and never returned.
	Key string `json:"key" maxLength:"16384"`
	// Domain, when set, must be one of the certificate's names: a check
	// that the right certificate was picked.
	Domain string `json:"domain,omitempty" maxLength:"253"`
}

// DomainCertificate is the uploaded certificate a domain is served with.
type DomainCertificate struct {
	ID       int64     `json:"id"`
	Issuer   string    `json:"issuer"`
	Public   bool      `json:"public"`
	NotAfter time.Time `json:"not_after"`
}
