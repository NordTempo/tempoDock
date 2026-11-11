package mcp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// list_vulnerabilities answers "what is vulnerable and is there a fix"
// once the live image is scanned (ADR 0074). The agent's token is
// limited to staging, which is all it sees.
func TestListVulnerabilities(t *testing.T) {
	f := newFixture(t)
	f.rt.SetScanFindings("nginx", []node.Finding{
		{ID: "CVE-2026-31337", Severity: node.SeverityCritical, Package: "openssl", Version: "3.3.1-r0", Type: "apk", FixedIn: "3.3.2-r0", KEV: true, EPSS: -1},
	})
	s := f.connect("shop-agent", nil)
	call(t, s, "deploy_image", map[string]any{"service": "web", "image": "nginx:1"})
	deadline := time.Now().Add(5 * time.Second)
	var res called
	for {
		res = call(t, s, "list_vulnerabilities", map[string]any{})
		if strings.Contains(res.summary, "CVE-2026-31337") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(res.summary, "CVE-2026-31337 critical, known exploited: openssl 3.3.1-r0 in shop/staging/web; fixed in 3.3.2-r0") {
		t.Fatalf("summary = %q", res.summary)
	}
	svc := call(t, s, "list_vulnerabilities", map[string]any{"service": "web"})
	if !strings.Contains(svc.summary, "To fix 1 critical, rebuild on a newer base image") || !strings.Contains(svc.summary, "openssl 3.3.1-r0 to 3.3.2-r0") {
		t.Fatalf("service summary = %q", svc.summary)
	}
}
