package build

import (
	"os"
	"testing"
)

// afterTests run once all tests are done, e.g. to remove containers the
// integration tests share.
var afterTests []func()

// TestMain lets the test binary stand in for the dock binary when Build
// re-executes itself to generate a Railpack plan.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == PlanCommand {
		os.Exit(PlanMain())
	}
	code := m.Run()
	for _, f := range afterTests {
		f()
	}
	os.Exit(code)
}
