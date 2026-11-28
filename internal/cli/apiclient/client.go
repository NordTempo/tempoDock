// Package apiclient is the CLI's generic HTTP client for the platform API:
// base URL and bearer token from the resolved context, JSON helpers, RFC 7807
// problem decoding into exit-coded errors, and a Server-Sent Events reader.
// It has no endpoint-specific methods; commands build those on top.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/version"
)

// UserAgent identifies the CLI and its version to the server.
var UserAgent = "dock-cli/" + version.Version

// Client talks to one server with one token.
type Client struct {
	// Org, when set, is sent as the `org` parameter of every request: the
	// organization to act in.
	// ProjectID constrains project-path requests from an ID-linked directory.
	ProjectID string
	Org       string
	// Reason, when set, says why the caller makes its changes (the
	// Dock-Reason header); an access request a change files shows it.
	Reason string
	// BeforeChange, when set, runs once, before the first request that
	// may change something (any method but GET and HEAD) is built. The
	// CLI uses it to name a remote server before acting on it.
	BeforeChange func()
	changeOnce   sync.Once

	base  *url.URL
	token string
	// platformToken, when set, retries a call the server refused because
	// it runs the platform (ADR 0080): the context's platform login.
	platformToken string
	http          *http.Client
	// upgrades is the HTTP/1.1-only client of Upgrade, made on first use.
	upgrades     *http.Client
	upgradesOnce sync.Once
}

// New returns a client for server (an http or https URL, optionally with a
// path prefix). An empty token sends no Authorization header.
func New(server, token string) (*Client, error) {
	u, err := ParseServerURL(server)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Fail fast when the server is down; streams may run for a long time,
	// so there is deliberately no overall request timeout.
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	// Redirects are never followed: the token must only ever go to the
	// server the user named, and an API that moved should be logged in to
	// again at its new URL. Do turns a 3xx into an error naming the target.
	noRedirects := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: u, token: token, http: &http.Client{Transport: tr, CheckRedirect: noRedirects}}, nil
}

// ParseServerURL validates a server URL as typed by a user and normalises
// it (no trailing slash).
func ParseServerURL(s string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(s, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, &output.Error{
			Exit:    output.ExitUsage,
			Message: fmt.Sprintf("invalid server URL %q", s),
			Hint:    "Use a full URL such as https://dock.example.com.",
		}
	}
	return u, nil
}

// Remote reports whether the server is on another machine: its host is
// not localhost or a loopback address.
func (c *Client) Remote() bool {
	host := c.base.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// Plaintext reports whether the server URL is plain http: the token and
// everything sent go unencrypted unless a tunnel encrypts them.
func (c *Client) Plaintext() bool {
	return c.base.Scheme == "http"
}

// NewRequest builds a request for path (which may include a query string),
// relative to the server URL, with auth and User-Agent set. The first one
// with a method other than GET or HEAD runs BeforeChange.
func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c.BeforeChange != nil && method != http.MethodGet && method != http.MethodHead {
		c.changeOnce.Do(c.BeforeChange)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/" + strings.TrimLeft(ref.Path, "/")
	u.RawQuery = ref.RawQuery
	if q := ref.Query(); c.Org != "" && !q.Has("org") {
		q.Set("org", c.Org)
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if c.ProjectID != "" {
		req.Header.Set("Dock-Project-ID", c.ProjectID)
	}
	if c.Reason != "" {
		req.Header.Set("Dock-Reason", c.Reason)
	}
	req.Header.Set("Accept", "application/json, application/problem+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// Do sends req. Any non-2xx response is returned as an *output.Error (with
// the decoded *Problem as its cause) and its body is closed; on success the
// caller closes resp.Body. Transport failures become ExitUnreachable.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &output.Error{
			Exit:    output.ExitUnreachable,
			Message: fmt.Sprintf("could not reach %s", c.base.Redacted()),
			Hint:    "Check that the server is running and the URL is right: `dock context current`.",
			Err:     err,
		}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, redirectError(req, resp)
	}
	perr := problemError(resp)
	if retry := c.asPlatform(req, perr); retry != nil {
		_ = resp.Body.Close()
		return c.Do(retry)
	}
	return nil, perr
}

// SetPlatformToken gives the client the context's platform token: a
// call the server refuses because it runs the platform is sent again
// with it, once (ADR 0080). Org calls never use it.
func (c *Client) SetPlatformToken(token string) { c.platformToken = token }

// asPlatform is req again with the platform token, when err is the
// server's refusal of a platform call made with another credential and
// the body can be sent again; nil otherwise.
func (c *Client) asPlatform(req *http.Request, err error) *http.Request {
	p, ok := errors.AsType[*Problem](err)
	if !ok || p.Refusal == nil || p.Refusal.DecidedBy != apitypes.RefusedPlatform || p.Refusal.Need == "org" ||
		c.platformToken == "" || req.Header.Get("Authorization") == "Bearer "+c.platformToken {
		return nil
	}
	retry := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil
		}
		body, err := req.GetBody()
		if err != nil {
			return nil
		}
		retry.Body = body
	}
	retry.Header.Set("Authorization", "Bearer "+c.platformToken)
	return retry
}

// redirectError reports a redirect, which the client never follows.
func redirectError(req *http.Request, resp *http.Response) error {
	target := resp.Header.Get("Location")
	if loc, err := resp.Location(); err == nil {
		target = loc.Redacted()
	}
	if target == "" {
		target = "(no Location header)"
	}
	return &output.Error{
		Exit:    output.ExitError,
		Message: fmt.Sprintf("%s redirected to %s; API requests don't follow redirects", req.URL.Redacted(), target),
		Hint:    "If the server moved (say, to https), log in at its new URL: `dock login <server-url>`.",
	}
}

// JSON sends in (if non-nil) as a JSON body and decodes the response into
// out (if non-nil).
func (c *Client) JSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.NewRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return &output.Error{
			Exit:    output.ExitError,
			Message: fmt.Sprintf("unexpected response from %s %s", method, path),
			Hint:    "The server may run a different version than this CLI (" + version.Version + "). Check `dock --version` against the server.",
			Err:     err,
		}
	}
	return nil
}

// Get decodes a JSON GET response into a T.
func Get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var v T
	err := c.JSON(ctx, http.MethodGet, path, nil, &v)
	return v, err
}

// Events opens a Server-Sent Events stream at path. Close the reader when
// done.
func (c *Client) Events(ctx context.Context, path string) (*EventReader, error) {
	req, err := c.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		_ = resp.Body.Close()
		return nil, &output.Error{Exit: output.ExitIncompatible, Message: fmt.Sprintf("expected an event stream from %s, got %q", path, ct)}
	}
	return NewEventReader(resp.Body), nil
}
