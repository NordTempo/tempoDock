package mcp_test

import (
	"context"
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcp"
)

// Every tool declares the permission its work needs (ADR 0080), and a
// tool that changes something names one Viewers don't hold, so an agent
// with view grants isn't offered it. Waking a sleeping environment is the
// exception: anyone who sees it may wake it, as read could.
func TestToolsDeclarePermissions(t *testing.T) {
	f := newFixture(t)
	tools, err := f.connect("admin", nil).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tl := range tools.Tools {
		listed[tl.Name] = true
		p, ok := mcp.ToolPerms[tl.Name]
		if !ok {
			if !slices.Contains(mcp.ToolsForAll, tl.Name) {
				t.Errorf("tool %s declares no permission: add it to toolPerms (perms.go)", tl.Name)
			}
			continue
		}
		readOnly := tl.Annotations != nil && tl.Annotations.ReadOnlyHint
		if !readOnly && tl.Name != "wake_environment" && core.ScopeHolds(apitypes.ScopeRead, p) {
			t.Errorf("tool %s changes something, but its permission %s is one Viewers hold", tl.Name, p)
		}
	}
	for name := range mcp.ToolPerms {
		if !listed[name] {
			t.Errorf("toolPerms names %s, which isn't a tool (an admin lists every one)", name)
		}
	}
}
