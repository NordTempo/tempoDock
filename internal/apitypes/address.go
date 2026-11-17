package apitypes

import "time"

// Address is where Dock itself is reached (ADR 0029): its public address,
// when an operator set one, and the private one it always has.
type Address struct {
	// Hostname is the public address, e.g. "dock.example.com"; empty when
	// Dock is only reachable at PrivateURL.
	Hostname string `json:"hostname"`
	// URL is https://<hostname>, or http:// when the control plane serves
	// apps over plain HTTP. Empty without a hostname.
	URL string `json:"url,omitempty" example:"https://dock.example.com"`
	// PrivateURL is the API's own listener ([listen] in config.toml),
	// typically on Tailscale. It keeps working with or without a public
	// address.
	PrivateURL string `json:"private_url" example:"http://100.64.0.10:7070"`
	// Status is checked when the address is read: DNS, then the
	// certificate, like a domain's. Empty without a hostname.
	Status       DomainStatus `json:"status,omitempty" enum:"waiting_for_dns,issuing_certificate,active,error"`
	StatusReason string       `json:"status_reason,omitempty"`
	// DNSRecords are the records to create; any one of them is enough.
	DNSRecords  []DNSRecord `json:"dns_records"`
	ResolvedIPs []string    `json:"resolved_ips,omitempty"`
	CheckedAt   *time.Time  `json:"checked_at,omitempty"`
	// Wildcards are the certificates of the base domains direct servers
	// generate hostnames under (ADR 0090), one per base domain.
	Wildcards []Wildcard `json:"wildcards"`
}

// WildcardStatus is where a base domain's wildcard certificate is.
type WildcardStatus string

const (
	// WildcardActive: issued and served; Reason, when set, is why its
	// renewal is failing.
	WildcardActive WildcardStatus = "active"
	// WildcardIssuing: being obtained for the first time.
	WildcardIssuing WildcardStatus = "issuing"
	// WildcardFailed: none yet, and the last attempt failed (Reason).
	// Generated hostnames get a certificate each meanwhile.
	WildcardFailed WildcardStatus = "failed"
	// WildcardUnavailable: none can be obtained (Reason: the base domain
	// isn't in a zone of the platform's Cloudflare connections).
	// Generated hostnames get a certificate each.
	WildcardUnavailable WildcardStatus = "unavailable"
)

// Wildcard is a base domain's wildcard certificate, for *.<base_domain>
// and <base_domain>, obtained over ACME DNS-01 through Cloudflare. With
// one, generated hostnames never appear in certificate-transparency logs.
type Wildcard struct {
	BaseDomain string `json:"base_domain" example:"apps.example.com"`
	// Servers are the direct servers whose base domain it is.
	Servers []string       `json:"servers"`
	Status  WildcardStatus `json:"status" enum:"active,issuing,failed,unavailable"`
	Reason  string         `json:"reason,omitempty"`
	// NotAfter is when the certificate expires, RenewAt when Dock renews
	// it; RetryAt the next attempt after a failure.
	NotAfter *time.Time `json:"not_after,omitempty"`
	RenewAt  *time.Time `json:"renew_at,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
}

// SetAddressRequest sets Dock's public address.
type SetAddressRequest struct {
	Hostname string `json:"hostname" maxLength:"253" example:"dock.example.com"`
}
