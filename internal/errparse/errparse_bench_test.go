package errparse

import "testing"

// benchLines are a service's ordinary output: access lines, JSON info
// lines and startup chatter, without an error among them.
var benchLines = []string{
	`10.0.0.1 - - [07/Nov/2026:12:00:00 +0000] "GET /api/cart HTTP/1.1" 200 612 "-" "Mozilla/5.0"`,
	`{"level":"info","ts":1730980800.5,"logger":"http.log.access","msg":"handled request","request":{"method":"GET","uri":"/api/cart","headers":{"User-Agent":["curl/8.0"]}},"status":200,"duration":0.0012}`,
	`time=2026-11-07T12:00:00Z level=info msg="cache warm" keys=1200 took=31ms`,
	"2026-11-07T12:00:00.000Z\tINFO\tworker\tprocessed batch\t{\"n\":50}",
	`listening on :8080, press ctrl-c to stop`,
}

// BenchmarkAssembler is the cost per line of following a container's
// output for errors when it has none: a byte scan, no allocation.
func BenchmarkAssembler(b *testing.B) {
	var a Assembler
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if _, ok := a.Line(benchLines[i%len(benchLines)]); ok {
			b.Fatal("an event from a plain line")
		}
		i++
	}
}

// BenchmarkAssemblerError is the cost of one error with a JavaScript
// stack of eight frames: assembling, normalizing and fingerprinting it.
func BenchmarkAssemblerError(b *testing.B) {
	trace := []string{
		"TypeError: Cannot read properties of undefined (reading 'price')",
		"    at Cart.total (/app/src/cart.js:42:13)",
		"    at Array.map (<anonymous>)",
		"    at render (/app/src/view.js:88:20)",
		"    at handler (/app/src/server.js:10:5)",
		"    at Layer.handle (/app/node_modules/express/lib/router/layer.js:95:5)",
		"    at next (/app/node_modules/express/lib/router/route.js:149:13)",
		"    at Route.dispatch (/app/node_modules/express/lib/router/route.js:119:3)",
		"    at process.processTicksAndRejections (node:internal/process/task_queues:95:5)",
	}
	var a Assembler
	b.ReportAllocs()
	for b.Loop() {
		for _, l := range trace {
			a.Line(l)
		}
		if _, ok := a.Flush(); !ok {
			b.Fatal("no event")
		}
	}
}
