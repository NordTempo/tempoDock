package logarchive

import (
	"context"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// BenchmarkArchivedLogsTail reads the last 100 lines of a release that
// wrote 100,000 (a busy container's archive): the path of `dock logs
// --release N` and the UI's release picker.
func BenchmarkArchivedLogsTail(b *testing.B) {
	root := b.TempDir()
	write(b, root, "1", "2", node.LogLimits{}, lines(0, 100_000))
	q := node.LogArchiveQuery{Service: "1", Release: "2", Tail: 100}
	b.ReportAllocs()
	for b.Loop() {
		ch, err := Read(context.Background(), root, q)
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for range ch {
			n++
		}
		if n != 100 {
			b.Fatalf("%d lines", n)
		}
	}
}
