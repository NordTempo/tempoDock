package remote

import (
	"context"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Host checks over the link (ADR 0109): the control plane asks a server
// to read its own host, and gets the results back as they are.

// HostCheckProtocol is the first protocol whose nodes check their host.
const HostCheckProtocol = 38

// opCheckHost is node.HostChecker.CheckHost, protocol 38.
const opCheckHost = "check-host"

// checkHostTimeout bounds CheckHost: the simulated apt upgrade is the
// slow part, a second or two, bounded at a minute on the node.
const checkHostTimeout = 2 * time.Minute

// maxHostChecks caps the results a node may send: ten checks, a few
// dozen ports and containers at most on any sane host. More is refused.
const maxHostChecks = 500

// wireHostCheck is node.HostCheck on the wire.
type wireHostCheck struct {
	Check    string `json:"check"`
	Subject  string `json:"subject,omitempty"`
	Status   string `json:"status"`
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Fix      string `json:"fix,omitempty"`
}

// checkHostResult is check-host's answer.
type checkHostResult struct {
	Checks capped[wireHostCheck] `json:"checks"`
}

// CheckHost implements node.HostChecker.
func (c *Client) CheckHost(ctx context.Context) ([]node.HostCheck, error) {
	if err := c.needProtocol(ctx, HostCheckProtocol, "check its host"); err != nil {
		return nil, err
	}
	r, err := callWithLogs[checkHostResult](ctx, c, opCheckHost, checkHostTimeout, empty{}, nil, nil)
	if err != nil {
		return nil, err
	}
	// A cut list would read as a complete check with findings missing:
	// refuse it, and the check before it stays with this error.
	if len(r.Checks) > maxHostChecks {
		return nil, fmt.Errorf("server sent %d host check results, over the limit of %d", len(r.Checks), maxHostChecks)
	}
	out := make([]node.HostCheck, len(r.Checks))
	for i, w := range r.Checks {
		out[i] = node.HostCheck{
			Check: clipText(w.Check, 64), Subject: clipText(w.Subject, 256), Status: node.HostCheckStatus(w.Status), Severity: w.Severity,
			Title: clipText(w.Title, 300), Detail: clipText(w.Detail, 2000), Fix: clipText(w.Fix, 1000),
		}
	}
	return out, nil
}

// checkHost is the node's side of check-host.
func checkHost(ctx context.Context, rt node.Runtime) (checkHostResult, error) {
	res, err := rt.CheckHost(ctx)
	out := checkHostResult{Checks: make(capped[wireHostCheck], len(res))}
	for i, h := range res {
		out.Checks[i] = wireHostCheck{Check: h.Check, Subject: h.Subject, Status: string(h.Status), Severity: h.Severity, Title: h.Title, Detail: h.Detail, Fix: h.Fix}
	}
	return out, err
}

// clipText shortens what a node sent to at most n bytes: the control
// plane trusts nodes less than itself (ADR 0014) and stores this.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
