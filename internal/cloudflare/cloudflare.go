// Package cloudflare is the little of Cloudflare's v4 API that Dock uses
// (ADR 0036): checking an API token, listing its zones, managing DNS
// records and remotely managed tunnels. It knows nothing about Dock.
//
// The token goes in the Authorization header only, never into errors or
// logs. Every call is bounded by the caller's context; Client adds no
// retries: the caller decides what a failure means (core keeps a queue).
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/NordTempo/tempoDock/internal/version"
)

// DefaultAPI is Cloudflare's API base URL.
const DefaultAPI = "https://api.cloudflare.com/client/v4"

const (
	// maxBody caps what is read of a response.
	maxBody = 1 << 20
	// maxPages caps paging, so a huge account can't keep a call going.
	maxPages = 40
	// pageSize is the page size of list calls.
	pageSize = 50
)

// defaultHTTP is the HTTP client of Clients that don't set one.
var defaultHTTP = &http.Client{}

// Client calls Cloudflare's API with one API token.
type Client struct {
	// API is the base URL; "" is DefaultAPI. Tests point it at a fake.
	API string
	// Token is the API token, sent as a bearer token.
	Token string
	// HTTP sends the requests; nil uses a shared default client.
	HTTP *http.Client
}

// Error is a call Cloudflare refused, or one that didn't reach it
// (Status 0).
type Error struct {
	// Op names the call, like "list zones".
	Op string
	// Status is the HTTP status; 0 when Cloudflare wasn't reached.
	Status int
	// Codes are Cloudflare's error codes.
	Codes []int
	// Message is Cloudflare's error messages, or the network error.
	Message string
	// RetryAfter is Cloudflare's Retry-After on a 429.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s: could not reach Cloudflare: %s", e.Op, e.Message)
	}
	return fmt.Sprintf("%s: Cloudflare answered %d: %s", e.Op, e.Status, e.Message)
}

// Has reports whether Cloudflare named code among the errors.
func (e *Error) Has(code int) bool {
	for _, c := range e.Codes {
		if c == code {
			return true
		}
	}
	return false
}

// Temporary reports whether trying again later may work: Cloudflare
// unreachable, rate limited, or failing on its side.
func (e *Error) Temporary() bool {
	return e.Status == 0 || e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Denied reports whether Cloudflare refused the token: invalid, expired
// or revoked, or lacking a permission (Cloudflare doesn't tell these
// apart reliably; VerifyToken does).
func (e *Error) Denied() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// NotFound reports a 404.
func (e *Error) NotFound() bool { return e.Status == http.StatusNotFound }

// AsError returns err's *Error, if it is one.
func AsError(err error) (*Error, bool) {
	return errors.AsType[*Error](err)
}

// Account is a Cloudflare account the token can reach.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Zone is a domain on Cloudflare.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Record is a DNS record.
type Record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	// TTL is 1 for automatic.
	TTL     int    `json:"ttl,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// Tunnel is a Cloudflare Tunnel.
type Tunnel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	AccountTag string `json:"account_tag"`
	// ConfigSrc is "cloudflare" for remotely managed tunnels (their
	// ingress is set through the API) and "local" for ones configured by
	// a file next to cloudflared.
	ConfigSrc string `json:"config_src"`
	// Status is inactive, degraded, healthy or down.
	Status    string     `json:"status"`
	DeletedAt *time.Time `json:"deleted_at"`
}

// Hostname is the tunnel's CNAME target, <id>.cfargotunnel.com.
func (t Tunnel) Hostname() string { return t.ID + ".cfargotunnel.com" }

// IngressRule routes requests for Hostname (all of them when empty) to
// Service, like "http://127.0.0.1:80".
type IngressRule struct {
	Hostname string `json:"hostname,omitempty"`
	Service  string `json:"service"`
}

// TunnelConfig is a remotely managed tunnel's configuration.
type TunnelConfig struct {
	Ingress []IngressRule `json:"ingress"`
}

// Connector is a cloudflared process running a tunnel.
type Connector struct {
	ID      string    `json:"id"`
	Version string    `json:"version"`
	RunAt   time.Time `json:"run_at"`
	Conns   []struct {
		ColoName string `json:"colo_name"`
	} `json:"conns"`
}

// envelope is Cloudflare's response wrapper.
type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiMessage    `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type apiMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Accounts lists the accounts the token can reach.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	return list[Account](ctx, c, "list accounts", "/accounts", nil)
}

// VerifyToken checks that the token is active. User tokens verify at
// /user/tokens/verify; account-owned ones only at their account's, so
// that one is tried next when accountID is known.
func (c *Client) VerifyToken(ctx context.Context, accountID string) error {
	var res struct {
		Status string `json:"status"`
	}
	err := c.do(ctx, "verify the token", http.MethodGet, "/user/tokens/verify", nil, nil, &res)
	if e, ok := AsError(err); ok && e.Denied() && accountID != "" {
		// Only a refusal there too, or success, counts: anything else
		// (a 404 for a user token) keeps the first refusal.
		aerr := c.do(ctx, "verify the token", http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/tokens/verify", nil, nil, &res)
		if ae, ok := AsError(aerr); aerr == nil || (ok && (ae.Denied() || ae.Temporary())) {
			err = aerr
		}
	}
	if err != nil {
		return err
	}
	if res.Status != "active" {
		return &Error{Op: "verify the token", Status: http.StatusUnauthorized, Message: "the token is " + cleanLine(res.Status, 40)}
	}
	return nil
}

// Zones lists the account's zones the token can read.
func (c *Client) Zones(ctx context.Context, accountID string) ([]Zone, error) {
	return list[Zone](ctx, c, "list zones", "/zones", url.Values{"account.id": {accountID}})
}

// CheckDNS reads one page of a zone's records, to see that the token may.
func (c *Client) CheckDNS(ctx context.Context, zoneID string) error {
	var out []Record
	return c.do(ctx, "list DNS records", http.MethodGet, zonePath(zoneID)+"/dns_records", url.Values{"per_page": {"5"}}, nil, &out)
}

// RecordsNamed lists every record named exactly name, of any type.
func (c *Client) RecordsNamed(ctx context.Context, zoneID, name string) ([]Record, error) {
	return list[Record](ctx, c, "list DNS records for "+name, zonePath(zoneID)+"/dns_records", url.Values{"name.exact": {name}})
}

// CreateRecord creates r.
func (c *Client) CreateRecord(ctx context.Context, zoneID string, r Record) (Record, error) {
	var out Record
	r.ID = ""
	err := c.do(ctx, "create the "+r.Type+" record "+r.Name, http.MethodPost, zonePath(zoneID)+"/dns_records", nil, r, &out)
	return out, err
}

// UpdateRecord changes record r.ID to r.
func (c *Client) UpdateRecord(ctx context.Context, zoneID string, r Record) (Record, error) {
	var out Record
	err := c.do(ctx, "change the record "+r.Name, http.MethodPatch, zonePath(zoneID)+"/dns_records/"+url.PathEscape(r.ID), nil, r, &out)
	return out, err
}

// DeleteRecord deletes a record; one that is already gone is no error.
func (c *Client) DeleteRecord(ctx context.Context, zoneID, id string) error {
	err := c.do(ctx, "delete a DNS record", http.MethodDelete, zonePath(zoneID)+"/dns_records/"+url.PathEscape(id), nil, nil, nil)
	if e, ok := AsError(err); ok && e.NotFound() {
		return nil
	}
	return err
}

// CheckTunnels reads one page of the account's tunnels, to see that the
// token may.
func (c *Client) CheckTunnels(ctx context.Context, accountID string) error {
	var out []Tunnel
	return c.do(ctx, "list tunnels", http.MethodGet, tunnelsPath(accountID), url.Values{"per_page": {"1"}, "is_deleted": {"false"}}, nil, &out)
}

// TunnelNamed returns the account's tunnel named name that isn't
// deleted, if there is one.
func (c *Client) TunnelNamed(ctx context.Context, accountID, name string) (*Tunnel, error) {
	var out []Tunnel
	if err := c.do(ctx, "find tunnel "+name, http.MethodGet, tunnelsPath(accountID), url.Values{"name": {name}, "is_deleted": {"false"}}, nil, &out); err != nil {
		return nil, err
	}
	for _, t := range out {
		if t.Name == name && t.DeletedAt == nil {
			return &t, nil
		}
	}
	return nil, nil
}

// Tunnel returns one tunnel.
func (c *Client) Tunnel(ctx context.Context, accountID, id string) (Tunnel, error) {
	var out Tunnel
	err := c.do(ctx, "read tunnel "+id, http.MethodGet, tunnelsPath(accountID)+"/"+url.PathEscape(id), nil, nil, &out)
	return out, err
}

// CreateTunnel creates a remotely managed tunnel.
func (c *Client) CreateTunnel(ctx context.Context, accountID, name string) (Tunnel, error) {
	var out Tunnel
	body := map[string]string{"name": name, "config_src": "cloudflare"}
	err := c.do(ctx, "create tunnel "+name, http.MethodPost, tunnelsPath(accountID), nil, body, &out)
	return out, err
}

// TunnelToken returns the token cloudflared runs a tunnel with.
func (c *Client) TunnelToken(ctx context.Context, accountID, id string) (string, error) {
	var out string
	err := c.do(ctx, "read the tunnel's token", http.MethodGet, tunnelsPath(accountID)+"/"+url.PathEscape(id)+"/token", nil, nil, &out)
	return out, err
}

// TunnelConfig returns a remotely managed tunnel's configuration.
func (c *Client) TunnelConfig(ctx context.Context, accountID, id string) (TunnelConfig, error) {
	var out struct {
		Config *TunnelConfig `json:"config"`
	}
	err := c.do(ctx, "read the tunnel's routes", http.MethodGet, tunnelsPath(accountID)+"/"+url.PathEscape(id)+"/configurations", nil, nil, &out)
	if out.Config == nil {
		return TunnelConfig{}, err
	}
	return *out.Config, err
}

// SetTunnelConfig replaces a remotely managed tunnel's configuration.
func (c *Client) SetTunnelConfig(ctx context.Context, accountID, id string, cfg TunnelConfig) error {
	body := map[string]TunnelConfig{"config": cfg}
	return c.do(ctx, "set the tunnel's routes", http.MethodPut, tunnelsPath(accountID)+"/"+url.PathEscape(id)+"/configurations", nil, body, nil)
}

// Connectors lists the cloudflared processes running a tunnel.
func (c *Client) Connectors(ctx context.Context, accountID, id string) ([]Connector, error) {
	var out []Connector
	err := c.do(ctx, "list the tunnel's connectors", http.MethodGet, tunnelsPath(accountID)+"/"+url.PathEscape(id)+"/connections", nil, nil, &out)
	return out, err
}

// DeleteTunnel drops a tunnel's connectors (Cloudflare refuses to delete
// a tunnel with active ones), then deletes it. One that is already gone
// is no error.
func (c *Client) DeleteTunnel(ctx context.Context, accountID, id string) error {
	path := tunnelsPath(accountID) + "/" + url.PathEscape(id)
	err := c.do(ctx, "drop the tunnel's connectors", http.MethodDelete, path+"/connections", nil, nil, nil)
	if e, ok := AsError(err); ok && e.NotFound() {
		return nil
	}
	if err != nil {
		return err
	}
	err = c.do(ctx, "delete the tunnel", http.MethodDelete, path, nil, nil, nil)
	if e, ok := AsError(err); ok && e.NotFound() {
		return nil
	}
	return err
}

func zonePath(zoneID string) string { return "/zones/" + url.PathEscape(zoneID) }

func tunnelsPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/cfd_tunnel"
}

// list fetches every page of a list call, up to maxPages.
func list[T any](ctx context.Context, c *Client, op, path string, q url.Values) ([]T, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", strconv.Itoa(pageSize))
	var all []T
	for page := 1; page <= maxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		var out []T
		pages, err := c.call(ctx, op, http.MethodGet, path, q, nil, &out)
		if err != nil {
			return nil, err
		}
		all = append(all, out...)
		if page >= pages || len(out) == 0 {
			break
		}
	}
	return all, nil
}

// do sends one request and decodes its result into out (nil ignores it).
func (c *Client) do(ctx context.Context, op, method, path string, q url.Values, body, out any) error {
	_, err := c.call(ctx, op, method, path, q, body, out)
	return err
}

// call sends one request and decodes its result into out, returning the
// number of pages a list has (1 when it doesn't say).
func (c *Client) call(ctx context.Context, op, method, path string, q url.Values, body, out any) (int, error) {
	base := c.API
	if base == "" {
		base = DefaultAPI
	}
	u := strings.TrimSuffix(base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", "tempo-dock/"+version.Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// The URL holds no secret, but the error is all we show.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, &Error{Op: op, Message: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	var env envelope
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&env)
	if resp.StatusCode/100 != 2 || (decodeErr == nil && !env.Success) {
		e := &Error{Op: op, Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		if e.Status/100 == 2 {
			e.Status = http.StatusBadRequest // success: false on a 2xx
		}
		var msgs []string
		for _, m := range env.Errors {
			e.Codes = append(e.Codes, m.Code)
			msgs = append(msgs, fmt.Sprintf("%s (%d)", cleanLine(m.Message, 200), m.Code))
		}
		if len(msgs) > 0 {
			e.Message = strings.Join(msgs, "; ")
		}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		return 0, e
	}
	if decodeErr != nil {
		return 0, &Error{Op: op, Status: resp.StatusCode, Message: "unreadable answer: " + decodeErr.Error()}
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return 0, &Error{Op: op, Status: resp.StatusCode, Message: "unexpected answer: " + err.Error()}
		}
	}
	pages := 1
	if env.ResultInfo != nil && env.ResultInfo.TotalPages > 0 {
		pages = env.ResultInfo.TotalPages
	}
	return pages, nil
}

// cleanLine makes s one printable line of at most n bytes, for error
// messages built from what Cloudflare sends.
func cleanLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, strings.TrimSpace(s))
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
