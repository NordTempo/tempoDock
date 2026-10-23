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
}

// SetAddressRequest sets Dock's public address.
type SetAddressRequest struct {
	Hostname string `json:"hostname" maxLength:"253" example:"dock.example.com"`
}
