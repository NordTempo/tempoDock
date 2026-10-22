package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const cliRuns = 30

// checkCLI times whole CLI invocations (fork, exec, run, exit) that need no
// network: the median of cliRuns after a few warm-up runs.
func checkCLI(o options, t *table) error {
	home, err := os.MkdirTemp("", "dock-perf-cli-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(home) }()
	env := append(cleanEnv(), "HOME="+home, "XDG_CONFIG_HOME="+home)
	for _, args := range [][]string{{"context", "list"}, {"--help"}} {
		var runs []time.Duration
		for i := range cliRuns + 3 {
			cmd := exec.Command(o.dock, args...)
			cmd.Env = env
			start := time.Now()
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("dock %s: %w: %s", strings.Join(args, " "), err, out)
			}
			if i >= 3 {
				runs = append(runs, time.Since(start))
			}
		}
		slices.Sort(runs)
		median := runs[len(runs)/2]
		t.withinTiming("CLI start-up: dock "+strings.Join(args, " "), ms(median)+" median", "≤ "+ms(cliBudget), median <= cliBudget)
	}
	return nil
}

// cleanEnv is the environment without DOCK_* and TEMPOBASE_* variables, so
// a developer's own context, token or config never leaks into a
// measurement.
func cleanEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "DOCK_") || strings.HasPrefix(kv, "TEMPOBASE_")
	})
}
