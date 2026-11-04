package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// pflag takes the first `quoted` word of a flag's usage as its value name,
// so backticks there garble --help.
func TestFlagUsageHasNoBackticks(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			if strings.Contains(f.Usage, "`") {
				t.Errorf("%s --%s: usage has a backtick: %q", c.CommandPath(), f.Name, f.Usage)
			}
		}
		c.LocalFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot(&globals{io: &output.IO{}}))
}
