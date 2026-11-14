//go:build linux

package main

import "testing"

// Only the daemon and the node agent start over without huge pages.
func TestLongRunning(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"dock", "serve", "--config", "/etc/dock/config.toml"}, true},
		{[]string{"dock", "node", "--config", "/etc/dock/config.toml"}, true},
		{[]string{"dock", "node"}, true},
		{[]string{"dock", "node", "join", "tok"}, false},
		{[]string{"dock", "deploy"}, false},
		{[]string{"dock"}, false},
	} {
		if got := longRunning(c.args); got != c.want {
			t.Errorf("longRunning(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
