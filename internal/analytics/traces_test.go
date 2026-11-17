package analytics

import "testing"

// A trace is listed once, by its edge span whichever clock started
// first, else by its earliest entry, in its first entry's place.
func TestOnePerTrace(t *testing.T) {
	edge := &TraceSummary{Trace: TraceID{1}, Span: 1, Kind: SpanEdge, Start: 1_000_400}
	app := &TraceSummary{Trace: TraceID{1}, Span: 2, Kind: SpanServer, Start: 1_000_000}
	outerCall := &TraceSummary{Trace: TraceID{2}, Span: 3, Kind: SpanServer, Start: 2_000_000}
	innerCall := &TraceSummary{Trace: TraceID{2}, Span: 4, Kind: SpanServer, Start: 2_000_100}
	for _, order := range [][]*TraceSummary{{app, innerCall, nil, edge, outerCall}, {edge, outerCall, app, innerCall}} {
		got := OnePerTrace(nil, order)
		if len(got) != 2 || got[0].Span != 1 || got[1].Span != 3 {
			t.Errorf("entries %v: listed %+v", order, got)
		}
	}
}
