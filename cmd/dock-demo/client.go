package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// client calls the demo daemon's API as the signed-in seed user, through
// a session cookie like the UI's (some calls are for people only), or
// with an API token when token is set.
type client struct {
	base  string
	http  *http.Client
	token string
	// org is the org to act in (the Dock-Org header), once there are two.
	org string
}

// newClient signs in as email and returns the client.
func newClient(ctx context.Context, base, email, password string) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	c := &client{base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	if err := c.do(ctx, http.MethodPost, "/auth/login", apitypes.LoginRequest{Email: email, Password: password}, nil); err != nil {
		return nil, fmt.Errorf("sign in as %s: %w", email, err)
	}
	return c, nil
}

// waitHealthy waits for the daemon to answer.
func waitHealthy(ctx context.Context, base string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/health", nil)
		if err != nil {
			return err
		}
		if res, err := http.DefaultClient.Do(req); err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("dock serve didn't answer at %s: %w", base, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// do sends body (JSON, or nothing when nil) to the API path and decodes
// the answer into out, if out isn't nil. A deploy still running answers
// 409; do waits for it and tries again, so releases can follow each other.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for try := 0; ; try++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, bytes.NewReader(b))
		if err != nil {
			return err
		}
		// The CSRF check wants the UI's origin on every change.
		req.Header.Set("Origin", c.base)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.org != "" {
			req.Header.Set("Dock-Org", c.org)
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		res, err := c.http.Do(req)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			return err
		}
		if res.StatusCode == http.StatusConflict && try < 50 && strings.Contains(string(data), "deploy") {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		if res.StatusCode >= 300 {
			return &apiError{Status: res.StatusCode, Msg: fmt.Sprintf("%s %s: %s: %s", method, path, res.Status, bytes.TrimSpace(data))}
		}
		if out == nil || len(data) == 0 {
			return nil
		}
		return json.Unmarshal(data, out)
	}
}

// apiError is an answer other than 2xx.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return e.Msg }
