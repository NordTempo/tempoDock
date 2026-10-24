package stats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// BenchmarkHostStats is one server sample with its memory breakdown, on a
// server with 40 systemd units and 50 containers already described.
func BenchmarkHostStats(b *testing.B) {
	root := b.TempDir()
	files := memoryTree()
	for i := range 40 {
		files[fmt.Sprintf("cgroup/system.slice/svc%02d.service/memory.stat", i)] = memStat((i+1)<<20, 0)
	}
	for i := range 50 {
		id := strings.Repeat("c", 62) + fmt.Sprintf("%02d", i)
		files["cgroup/system.slice/docker-"+id+".scope/memory.stat"] = memStat(32<<20, 0)
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	r := New(Options{Proc: root + "/proc", Cgroup: root + "/cgroup", Container: func(context.Context, string) (node.MemoryPart, error) {
		return node.MemoryPart{Group: node.MemoryApps, Name: "web-1", Service: "1"}, nil
	}})
	ctx := context.Background()
	if _, err := r.HostStats(ctx); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if h, err := r.HostStats(ctx); err != nil || len(h.MemoryParts) == 0 {
			b.Fatal(err)
		}
	}
}
