package traces

import (
	"fmt"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// A traced request becomes its trace's edge span, for the service and
// release its route's token names; a request without a valid token or
// IDs makes none, and a failed one rescues its trace's spans.
func TestEdgeSpans(t *testing.T) {
	in, sp := newIngest()
	at := time.Unix(1_800_000_000, 0)
	in.Now = func() time.Time { return at }
	e := &Edge{In: in}
	tok := Mint(testKey, testClaims)
	req := EdgeRequest{Token: tok, Method: "GET", Route: "/orders/:id", Status: 0, End: at, Duration: 40 * time.Millisecond,
		TraceID: "0102030405060708090a0b0c0d0e0f10", SpanID: "0a0b0c0d0e0f1011"}
	e.Request(req)
	for _, bad := range []EdgeRequest{{Token: "dtr1.nope", TraceID: req.TraceID, SpanID: req.SpanID}, {Token: tok, TraceID: "zz", SpanID: req.SpanID}} {
		e.Request(bad)
	}
	spans := sp.spans(t)
	if len(spans) != 1 {
		t.Fatalf("spans = %+v", spans)
	}
	s := spans[0]
	if s.Service != testClaims.Service || s.Release != testClaims.Release || s.Kind != analytics.SpanEdge || !s.Entry || s.Name != "GET /orders/:id" ||
		s.HTTPStatus != 200 || s.Duration != 40_000 || s.Start != at.Add(-40*time.Millisecond).UnixMicro() || s.ID != 0x0a0b0c0d0e0f1011 || s.Parent != 0 {
		t.Errorf("edge span = %+v", s)
	}
}

// Edge spans have a rate of their own: a flood of internet requests
// leaves the app's own spans their share.
func TestEdgeSpansHaveTheirOwnRate(t *testing.T) {
	in, sp := newIngest()
	in.Limits.ServiceRate = 1 // a burst of 2
	at := time.Unix(1_800_000_000, 0)
	in.Now = func() time.Time { return at }
	e := &Edge{In: in}
	tok := Mint(testKey, testClaims)
	for i := range 5 {
		e.Request(EdgeRequest{Token: tok, Method: "GET", Route: "/", Status: 200, End: at, Duration: time.Millisecond,
			TraceID: fmt.Sprintf("%032x", i+1), SpanID: "0a0b0c0d0e0f1011"})
	}
	if n := len(sp.spans(t)); n != 2 {
		t.Fatalf("edge spans kept %d, want the burst of 2", n)
	}
	if !in.rates.take(testClaims.Service, in.Limits.ServiceRate, 2*in.Limits.ServiceRate, at) {
		t.Error("the edge flood spent the app's own rate")
	}
}
