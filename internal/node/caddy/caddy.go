// Package caddy implements node.Proxy on top of a Caddy process.
//
// Caddy runs as its own systemd service. The daemon is the only thing that
// configures it: it renders the complete config from the routing table and
// POSTs it to Caddy's admin API on a Unix socket. Caddy validates the config
// and swaps it in atomically, or rejects it and keeps the old one.
//
// Certificates are issued on demand, the first time a hostname is requested
// over HTTPS, and only after the daemon's `ask` endpoint approves it.
//
// Reloads are close to, but not exactly, lossless: a request on a brand-new
// connection that races a reload can be dropped, and an HTTP/2 connection
// can keep using the old routes for a while. See
// TestIntegrationSwapUnderTraffic for details. Batch route changes rather
// than reloading in a tight loop.
package caddy

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Options configure the rendered Caddy config and how to reach Caddy.
type Options struct {
	// AdminSocket is the path of Caddy's admin Unix socket.
	AdminSocket string
	HTTPPort    int
	HTTPSPort   int
	// ACMEEmail is given to the certificate authority. Optional.
	ACMEEmail string
	// DisableTLS serves every route over plain HTTP on HTTPPort.
	DisableTLS bool
	// AskURL is the daemon endpoint Caddy asks before issuing a certificate,
	// e.g. http://127.0.0.1:7071/internal/caddy/ask. Required unless
	// DisableTLS is set.
	AskURL string
	// PublicIPs are the addresses DNS records should point at, used by
	// CheckDomain.
	PublicIPs []string

	// internalCA makes Caddy issue certificates from its own local CA
	// instead of ACME. Tests only, so they never contact a real CA.
	internalCA bool
	// gracePeriod overrides defaultGracePeriod. Tests only.
	gracePeriod time.Duration
}

func (o Options) validate() error {
	switch {
	case o.AdminSocket == "":
		return errors.New("caddy: admin socket path is required")
	case o.HTTPPort <= 0 || o.HTTPSPort <= 0:
		return errors.New("caddy: HTTP and HTTPS ports are required")
	case !o.DisableTLS && o.AskURL == "":
		return errors.New("caddy: an ask URL is required when TLS is enabled")
	}
	return nil
}

// Proxy drives one Caddy process through its admin socket.
type Proxy struct {
	opts   Options
	client *http.Client

	// Overridable in tests.
	lookupIP func(ctx context.Context, host string) ([]net.IPAddr, error)
	rootCAs  *x509.CertPool // nil means the system roots
}

var _ node.Proxy = (*Proxy)(nil)

// New returns a Proxy for the Caddy listening on opts.AdminSocket. It does
// not contact Caddy.
func New(opts Options) (*Proxy, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	var d net.Dialer
	return &Proxy{
		opts: opts,
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return d.DialContext(ctx, "unix", opts.AdminSocket)
				},
			},
			// A load waits for the new config to start and the old one to
			// stop, which can take a moment; never hang forever.
			Timeout: time.Minute,
		},
		lookupIP: net.DefaultResolver.LookupIPAddr,
	}, nil
}

// ApplyRoutes renders the full config for routes and loads it into Caddy.
// If Caddy rejects it, the previous config stays in effect.
func (p *Proxy) ApplyRoutes(ctx context.Context, routes []node.Route) error {
	cfg, err := Render(p.opts, routes)
	if err != nil {
		return err
	}
	return p.load(ctx, cfg)
}

// load POSTs cfg to Caddy's /load endpoint, which validates it and swaps
// it in atomically.
func (p *Proxy) load(ctx context.Context, cfg []byte) error {
	// The host is ignored; the transport always dials the admin socket.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://caddy/load", bytes.NewReader(cfg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy: reach admin socket %s: %w", p.opts.AdminSocket, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("caddy rejected the config: %s", e.Error)
	}
	return fmt.Errorf("caddy rejected the config: %s: %s", resp.Status, bytes.TrimSpace(body))
}
