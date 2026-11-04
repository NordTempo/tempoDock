package api

import (
	"net/http"
	"runtime"
	"testing"
)

// BenchmarkNewAPI builds the whole Huma API, as the daemon does at start-up.
// Besides the allocations it reports retained-B: the heap the built API
// keeps alive after a GC, which is what the idle daemon pays for it.
func BenchmarkNewAPI(b *testing.B) {
	b.ReportAllocs()
	var retained uint64
	var ms runtime.MemStats
	for b.Loop() {
		runtime.GC()
		runtime.ReadMemStats(&ms)
		before := ms.HeapAlloc
		a := newAPI(http.NewServeMux(), &Server{})
		runtime.GC()
		runtime.ReadMemStats(&ms)
		retained += ms.HeapAlloc - before
		runtime.KeepAlive(a)
	}
	b.ReportMetric(float64(retained)/float64(b.N), "retained-B")
}
