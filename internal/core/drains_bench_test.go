package core

import (
	"context"
	"testing"
	"time"
)

// drainBenchLine is a typical access log line.
const drainBenchLine = `203.0.113.7 - - [03/Nov/2026:10:12:01 +0000] "GET /api/products?page=2 HTTP/1.1" 200 5123 "-" "Mozilla/5.0"`

// BenchmarkDrainEnqueue is the cost a follower pays per line and drain:
// queueing it, never blocking. The sender is marked running, so none
// starts; the queue is emptied as it fills.
func BenchmarkDrainEnqueue(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Core{ctx: ctx}
	d := &drain{ctx: ctx, kick: make(chan struct{}, 1), running: true}
	r := drainRecord{Time: time.Now(), Stream: "stdout", Text: drainBenchLine, src: newLogSource("shop", "production", "web", 12, "vps-1")}
	b.ReportAllocs()
	for b.Loop() {
		c.enqueue(d, r)
		if len(d.queue) == drainQueueLines {
			d.queue, d.queued = d.queue[:0], 0
		}
	}
}

// drainBenchBatch is a full batch from two services.
func drainBenchBatch() []drainRecord {
	web := newLogSource("shop", "production", "web", 12, "vps-1")
	worker := newLogSource("shop", "production", "worker", 4, "vps-1")
	batch := make([]drainRecord, drainBatchLines)
	at := time.Now()
	for i := range batch {
		src, stream := web, "stdout"
		if i%4 == 0 {
			src, stream = worker, "stderr"
		}
		batch[i] = drainRecord{Time: at.Add(time.Duration(i)), Stream: stream, Text: drainBenchLine, src: src}
	}
	return batch
}

// BenchmarkDrainEncodeHTTP encodes a full batch (1,000 lines) as JSON
// lines into a reused buffer, as an http drain's sender does.
func BenchmarkDrainEncodeHTTP(b *testing.B) {
	batch := drainBenchBatch()
	buf := appendNDJSON(nil, batch)
	b.ReportAllocs()
	for b.Loop() {
		buf = appendNDJSON(buf[:0], batch)
	}
}

// BenchmarkDrainEncodeLoki encodes a full batch as a Loki push request.
func BenchmarkDrainEncodeLoki(b *testing.B) {
	batch := drainBenchBatch()
	buf := appendLoki(nil, batch)
	b.ReportAllocs()
	for b.Loop() {
		buf = appendLoki(buf[:0], batch)
	}
}

// BenchmarkDrainEncodeSyslog frames a full batch for a tcp syslog drain.
func BenchmarkDrainEncodeSyslog(b *testing.B) {
	batch := drainBenchBatch()
	var msg, buf []byte
	b.ReportAllocs()
	for b.Loop() {
		buf = buf[:0]
		for _, r := range batch {
			msg = appendSyslog(msg[:0], r)
			buf = append(buf, msg...)
		}
	}
}
