package remote

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// The analytics calls over a real link (ADR 0037): salts go to the node,
// its spool streams to the control plane as it fills and is acknowledged,
// and the store on the node is reachable through the link.
func TestAnalyticsOverTheLink(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fk := h.rt.Runtime

	salt := bytes.Repeat([]byte{9}, 32)
	if err := h.client.SetAnalytics(ctx, node.AnalyticsConfig{Salts: map[int64][]byte{20391: salt}}); err != nil {
		t.Fatal(err)
	}
	if got := fk.AnalyticsSalts()[20391]; !bytes.Equal(got, salt) {
		t.Fatalf("the node got salts %v", fk.AnalyticsSalts())
	}

	fk.AppendSpool([]byte(`{"k":1,"p":"/a"}`), []byte(`{"k":1,"p":"/b"}`))
	tail, err := h.client.TailSpool(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := <-tail
	if b.Count != 2 || b.Position != 2 || string(b.Events) != "{\"k\":1,\"p\":\"/a\"}\n{\"k\":1,\"p\":\"/b\"}" {
		t.Fatalf("first batch = %+v (%q)", b, b.Events)
	}
	// Appended later: streamed without asking again.
	fk.AppendSpool([]byte(`{"k":1,"p":"/c"}`))
	if b = <-tail; b.Count != 1 || b.Position != 3 {
		t.Fatalf("second batch = %+v", b)
	}
	if err := h.client.AckSpool(ctx, b.Position, b.Count); err != nil || fk.SpoolAcked() != 3 {
		t.Fatalf("ack: %v, node acked %d", err, fk.SpoolAcked())
	}

	spec := &node.AnalyticsStoreSpec{Image: "dock-analytics:dev", Token: strings.Repeat("t", 32), MemoryBytes: 512 << 20}
	if err := h.client.EnsureAnalyticsStore(ctx, spec); err != nil || fk.AnalyticsStoreSpec() == nil || *fk.AnalyticsStoreSpec() != *spec {
		t.Fatalf("ensure: %v, node has %+v", err, fk.AnalyticsStoreSpec())
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return h.client.DialAnalyticsStore(ctx)
	}}}
	resp, err := hc.Get("http://analytics/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `"version":"fake"`) {
		t.Fatalf("through the link the store said %q", body)
	}

	// Removing the store: a nil spec.
	if err := h.client.EnsureAnalyticsStore(ctx, nil); err != nil || fk.AnalyticsStoreSpec() != nil {
		t.Fatalf("remove: %v, node has %+v", err, fk.AnalyticsStoreSpec())
	}
}
