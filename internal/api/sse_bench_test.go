package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// discardWriter is a flushable ResponseWriter that drops what is written,
// so the benchmark measures the SSE writer, not a growing buffer.
type discardWriter struct{ h http.Header }

func (w *discardWriter) Header() http.Header         { return w.h }
func (w *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *discardWriter) WriteHeader(int)             {}
func (w *discardWriter) Flush()                      {}

// BenchmarkSSEWrite writes already-encoded events to one client in batches,
// as /events does: one buffer fill, one write and one flush per batch.
func BenchmarkSSEWrite(b *testing.B) {
	data, err := json.Marshal(apitypes.Event{
		Type: apitypes.EventReleaseEvent, Time: time.Unix(1_800_000_000, 0).UTC(),
		Project: "big", Environment: "production", Service: "svc-00",
		ReleaseEvent: &apitypes.ReleaseEvent{Seq: 123456, ReleaseNumber: 42, Message: "health check passed on /healthz"},
	})
	if err != nil {
		b.Fatal(err)
	}
	for _, n := range []int{1, 16} {
		b.Run(fmt.Sprintf("batch=%d", n), func(b *testing.B) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
			w := &sseWriter{hctx: humatest.NewContext(&huma.Operation{}, req, &discardWriter{h: http.Header{}})}
			batch := make([]sseEvent, n)
			for i := range batch {
				batch[i] = sseEvent{name: string(apitypes.EventReleaseEvent), id: "123456", data: data}
			}
			w.ready()
			b.ReportAllocs()
			for b.Loop() {
				if err := w.write(batch...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
