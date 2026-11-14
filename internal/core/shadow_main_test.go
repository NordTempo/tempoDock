package core

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// TestMain runs the package's tests and fails them if the permission
// model disagreed with today's checks anywhere (ADR 0080's shadow): every
// test of a permission path is part of the equivalence test.
func TestMain(m *testing.M) {
	code := m.Run()
	if r := faults(ShadowReport()); code == 0 && len(r) > 0 {
		fmt.Fprintln(os.Stderr, "the permission model disagreed with today's checks:")
		for _, d := range r {
			fmt.Fprintf(os.Stderr, "  %d× %s\n", d.Count, d.Check)
		}
		code = 1
	}
	os.Exit(code)
}

// faults are the disagreements that are the model's or the mapping's
// fault: all but the data policy's lowering rules, which the migration's
// preflight stops on (TestShadowFindsLoweringRules checks they are seen).
func faults(r []apitypes.AccessDisagreement) []apitypes.AccessDisagreement {
	var out []apitypes.AccessDisagreement
	for _, d := range r {
		if !strings.Contains(d.Check, loweringRule) {
			out = append(out, d)
		}
	}
	return out
}
