package build

import (
	"os"
	"testing"
)

// TestMain lets the test binary stand in for the dock binary when Build
// re-executes itself to generate a Railpack plan.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == PlanCommand {
		os.Exit(PlanMain())
	}
	os.Exit(m.Run())
}
