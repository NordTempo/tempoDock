package docker

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// BenchmarkLineWriter splits a frame of 64 timestamped access log lines,
// as Logs does for every container a log drain or a viewer follows.
func BenchmarkLineWriter(b *testing.B) {
	var frame []byte
	for range 64 {
		frame = append(frame, `2026-11-03T10:12:01.123456789Z 203.0.113.7 - - [03/Nov/2026:10:12:01 +0000] "GET /api/products?page=2 HTTP/1.1" 200 5123 "-" "Mozilla/5.0"`+"\n"...)
	}
	w := &lineWriter{stream: "stdout", emit: func(node.LogLine) error { return nil }}
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		if _, err := w.Write(frame); err != nil {
			b.Fatal(err)
		}
	}
}
