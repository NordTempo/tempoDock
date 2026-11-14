package api

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"

	"github.com/NordTempo/tempoDock/internal/core"
)

// TestMain fails the package's tests if the permission model disagreed
// with today's checks anywhere (ADR 0080's shadow): the permission matrix
// and lock tests walk every operation, so they check the model too.
func TestMain(m *testing.M) {
	code := m.Run()
	if r := faults(core.ShadowReport()); code == 0 && len(r) > 0 {
		fmt.Fprintln(os.Stderr, "the permission model disagreed with today's checks:")
		for _, d := range r {
			fmt.Fprintf(os.Stderr, "  %d× %s\n", d.Count, d.Check)
		}
		code = 1
	}
	os.Exit(code)
}

// faults are the disagreements that are the model's or the mapping's
// fault: all but the data policy's lowering rules (see core's TestMain).
func faults(r []apitypes.AccessDisagreement) []apitypes.AccessDisagreement {
	var out []apitypes.AccessDisagreement
	for _, d := range r {
		if !strings.Contains(d.Check, "a lowering rule") {
			out = append(out, d)
		}
	}
	return out
}
