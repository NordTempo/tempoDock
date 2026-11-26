package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// A prune target that doesn't exist is a usage error that lists the
// ones that do, before anything is asked of a server.
func TestServerPruneUnknownTarget(t *testing.T) {
	code, _, stderr := exec(t, "server", "prune", "prod-1", "--only", "scanner,volumes", "--yes")
	if code != int(output.ExitUsage) || !strings.Contains(stderr, `"volumes"`) || !strings.Contains(stderr, "scan-db") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
