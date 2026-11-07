package apitypes

import "time"

// Cloudflare is a Cloudflare connection (ADR 0036): an API token with
// which Dock runs tunnels for tunnel servers and routes domains added
// from its zones through them. The token is never returned. A connection
// is the platform's (it runs server tunnels) or an org's own, whose
// zones only that org's domains use (ADR 0062). A zone may be on several
// connections.
type Cloudflare struct {
	Name string `json:"name" example:"home"`
	// Shared is set on a platform connection whose zones org domains may
	// use; never on an org's own.
	Shared      bool   `json:"shared"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	// Status is invalid once Cloudflare refused the token (revoked or
	// expired): its tunnels and records wait for a new one.
	Status       CloudflareStatus `json:"status" enum:"ok,invalid"`
	StatusReason string           `json:"status_reason,omitempty"`
	// Zones are the zones domains can be added from: those the token can
	// list and read DNS records of.
	Zones []string `json:"zones"`
	// UsedBy names the servers whose tunnels it runs.
	UsedBy    []string  `json:"used_by"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
	// ZonesAdded, ZonesDropped and DomainsFailed are set only in the
	// answer to a new token or a refresh: the zones it reaches now and
	// didn't before, those it no longer reaches, and the domains in
	// those that no other connection could take, which no longer route.
	ZonesAdded    []string `json:"zones_added,omitempty"`
	ZonesDropped  []string `json:"zones_dropped,omitempty"`
	DomainsFailed []string `json:"domains_failed,omitempty"`
}

// CloudflareStatus is whether a Cloudflare connection's token works.
type CloudflareStatus string

// Cloudflare connection statuses.
const (
	CloudflareOK      CloudflareStatus = "ok"
	CloudflareInvalid CloudflareStatus = "invalid"
)

// CreateCloudflareRequest adds a Cloudflare connection. Dock checks the
// token and lists its zones before saving it.
type CreateCloudflareRequest struct {
	Name  string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$"`
	Token string `json:"token" minLength:"1" doc:"API token: Account › Cloudflare Tunnel › Edit, Zone › DNS › Edit and Zone › Zone › Read"`
	// AccountID picks the account when the token reaches several, and is
	// needed for tokens owned by an account.
	AccountID string `json:"account_id,omitempty"`
	// Shared lets org domains use a platform connection's zones; only for
	// the platform's.
	Shared bool `json:"shared,omitempty" doc:"Platform connections only: organizations' domains may use its zones"`
}

// UpdateCloudflareRequest gives a Cloudflare connection a new token (a
// revoked or expired one, or new zones), checked like a new one, and on a
// platform connection shares it or stops sharing it. Without a token only
// Shared changes.
type UpdateCloudflareRequest struct {
	Token     string  `json:"token,omitempty"`
	AccountID *string `json:"account_id,omitempty" doc:"Needs a token"`
	Shared    *bool   `json:"shared,omitempty" doc:"Platform connections only: organizations' domains may use its zones"`
}

// CloudflareZone is a zone domains can be added from (ADR 0036): one of
// the org's own connections, or of a platform connection shared with
// orgs (Platform).
type CloudflareZone struct {
	Name       string `json:"name" example:"tempo.run"`
	Connection string `json:"connection"`
	Platform   bool   `json:"platform,omitempty" doc:"The platform's zone, shared with organizations, not the organization's own"`
}

// DNSRecordStatus is how far Dock is with routing a domain through a
// tunnel.
type DNSRecordStatus string

// Statuses: pending (Dock is on it, or retrying), ready (routed, its
// proxied CNAME made by Dock), external (a CNAME someone else made already
// points at the tunnel; Dock leaves it alone), error (Dock gave up; the
// reason says why), deleting.
const (
	DNSPending  DNSRecordStatus = "pending"
	DNSReady    DNSRecordStatus = "ready"
	DNSExternal DNSRecordStatus = "external"
	DNSError    DNSRecordStatus = "error"
	DNSDeleting DNSRecordStatus = "deleting"
)

// DomainDNS is how a domain added from Cloudflare is routed through its
// server's tunnel.
type DomainDNS struct {
	Connection string          `json:"connection"`
	Zone       string          `json:"zone"`
	Status     DNSRecordStatus `json:"status" enum:"pending,ready,external,error,deleting"`
	Reason     string          `json:"reason,omitempty"`
	// Tunnel is the CNAME target, <tunnel id>.cfargotunnel.com, once known.
	Tunnel string `json:"tunnel,omitempty"`
}

// CloudflareRecord is a domain Dock routes through a tunnel, for the
// console.
type CloudflareRecord struct {
	Hostname   string `json:"hostname" example:"shop.tempo.run"`
	Connection string `json:"connection"`
	// Server is the server whose tunnel it goes through; empty once removed.
	Server    string          `json:"server,omitempty"`
	Status    DNSRecordStatus `json:"status" enum:"pending,ready,external,error,deleting"`
	Reason    string          `json:"reason,omitempty"`
	Tunnel    string          `json:"tunnel,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// CloudflareRetried counts the records and tunnels set to be tried again.
type CloudflareRetried struct {
	Retried int `json:"retried"`
}

// TunnelStatus is how far Dock is with a server's tunnel.
type TunnelStatus string

// Tunnel statuses: pending (being created, or retrying), ready (created
// or adopted, and its token given to the server), error, deleting.
const (
	TunnelPending  TunnelStatus = "pending"
	TunnelReady    TunnelStatus = "ready"
	TunnelError    TunnelStatus = "error"
	TunnelDeleting TunnelStatus = "deleting"
)

// ServerTunnel is the Cloudflare tunnel Dock manages for a server.
type ServerTunnel struct {
	Connection string       `json:"connection"`
	TunnelID   string       `json:"tunnel_id,omitempty"`
	Hostname   string       `json:"hostname,omitempty" doc:"CNAME target, <tunnel_id>.cfargotunnel.com"`
	Status     TunnelStatus `json:"status" enum:"pending,ready,error,deleting"`
	Reason     string       `json:"reason,omitempty"`
	// Adopted: the tunnel was made outside Dock, which never deletes it.
	Adopted bool `json:"adopted"`
	// Connectors are the cloudflared processes running it, read from
	// Cloudflare when one server is shown; unset elsewhere or when
	// Cloudflare couldn't be asked.
	Connectors []TunnelConnector `json:"connectors,omitempty"`
}

// TunnelConnector is a cloudflared process running a tunnel.
type TunnelConnector struct {
	ID      string    `json:"id"`
	Version string    `json:"version"`
	Edge    []string  `json:"edge" doc:"Cloudflare data centres it is connected to"`
	Since   time.Time `json:"since"`
}

// ServerTunnelRequest has Dock manage a tunnel server's Cloudflare tunnel:
// create a new one, or adopt the one the server already runs (made in
// the dashboard). Adopting replaces the tunnel's routes with one to the
// server's Caddy only with ReplaceIngress.
type ServerTunnelRequest struct {
	Action         string `json:"action" enum:"create,adopt"`
	Cloudflare     string `json:"cloudflare,omitempty" doc:"Cloudflare connection; may be left out when there is one"`
	ReplaceIngress bool   `json:"replace_ingress,omitempty"`
}
