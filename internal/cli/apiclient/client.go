// Package apiclient is the CLI's generic HTTP client for the platform API:
// base URL and bearer token from the resolved context, JSON helpers, RFC 7807
// problem decoding into exit-coded errors, and a Server-Sent Events reader.
// It has no endpoint-specific methods; commands build those on top.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NordTempo/platform/internal/cli/output"
	"github.com/NordTempo/platform/internal/version"
)

// UserAgent identifies the CLI and its version to the server.
var UserAgent = "dock-cli/" + version.Version

// Client talks to one server with one token.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
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

// NewRequest builds a request for path (which may include a query string),
// relative to the server URL, with auth and User-Agent set.
func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	ref, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/" + strings.TrimLeft(ref.Path, "/")
	u.RawQuery = ref.RawQuery
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
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
	return nil, problemError(resp)
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
