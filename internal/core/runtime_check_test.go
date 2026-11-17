package core

import (
	"context"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
)

// A runtime that would panic on its first call, in a background loop as
// often as not (an image scan took an integration run down), is refused
// when Core is made: a node.Local missing a part, or a nil pointer in the
// interface. A nil hub gives no Nodes at all.
func TestNewRefusesRuntimesThatPanic(t *testing.T) {
	f := fake.New()
	whole := node.Local{Containers: f, Builder: f, Proxy: f, Stats: f, Traffic: f, Tunnel: f, Analytics: f, Prober: f, Scanner: f}
	if m := whole.Missing(); len(m) != 0 {
		t.Fatalf("a whole Local misses %v", m)
	}
	noScanner := whole
	noScanner.Scanner = nil
	for _, tc := range []struct {
		name string
		rt   node.Runtime
		want string
	}{
		{"a Local without a scanner", noScanner, "has no Scanner"},
		{"a nil pointer", (*fake.Runtime)(nil), "the runtime is a nil *fake.Runtime"},
	} {
		cfg := config.Default()
		cfg.DataDir = t.TempDir()
		_, err := New(context.Background(), Options{Config: cfg, Runtime: tc.rt})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: New = %v, want an error with %q", tc.name, err, tc.want)
		}
	}
	if n := NodesOf(nil); n != nil {
		t.Errorf("NodesOf(nil) = %#v, want nil", n)
	}
}
