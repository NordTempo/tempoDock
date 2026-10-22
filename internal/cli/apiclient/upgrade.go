package apiclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Upgrade sends GET path asking to switch to protocol (`Connection:
// Upgrade`, `Upgrade: <protocol>`) and returns the connection once the
// server answered 101: from then on it carries raw bytes both ways, and
// closing it ends them. Other answers are errors, as for Do. ctx bounds
// the request only.
func (c *Client) Upgrade(ctx context.Context, path, protocol string) (io.ReadWriteCloser, error) {
	c.upgradesOnce.Do(func() {
		// HTTP/1.1 only: an HTTP/2 connection can't switch protocols.
		tr := c.http.Transport.(*http.Transport).Clone()
		tr.ForceAttemptHTTP2 = false
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		c.upgrades = &http.Client{Transport: tr, CheckRedirect: c.http.CheckRedirect}
	})
	req, err := c.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", protocol)
	resp, err := c.upgrades.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &output.Error{
			Exit:    output.ExitUnreachable,
			Message: fmt.Sprintf("could not reach %s", c.base.Redacted()),
			Hint:    "Check that the server is running and the URL is right: `dock context current`.",
			Err:     err,
		}
	}
	if resp.StatusCode == http.StatusSwitchingProtocols && strings.EqualFold(resp.Header.Get("Upgrade"), protocol) {
		if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
			return rwc, nil
		}
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, redirectError(req, resp)
	case resp.StatusCode < 300:
		return nil, &output.Error{
			Exit:    output.ExitIncompatible,
			Message: fmt.Sprintf("the server answered %s instead of switching to %s", resp.Status, protocol),
			Hint:    "A proxy in front of the server may not pass upgrades on, or the server is older than this CLI.",
		}
	}
	return nil, problemError(resp)
}
