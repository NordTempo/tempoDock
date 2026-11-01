package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	tail, err := h.client.TailSpool(ctx, node.SpoolEvents, 0)
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
	if err := h.client.AckSpool(ctx, node.SpoolEvents, b.Position, b.Count); err != nil || fk.SpoolAcked() != 3 {
		t.Fatalf("ack: %v, node acked %d", err, fk.SpoolAcked())
	}
	// The replay spool streams apart from the events.
	fk.AppendReplay([]byte(`{"g":7,"q":0}`))
	replay, err := h.client.TailSpool(ctx, node.SpoolReplay, 0)
	if err != nil {
		t.Fatal(err)
	}
	if b := <-replay; b.Count != 1 || string(b.Events) != `{"g":7,"q":0}` {
		t.Fatalf("replay batch = %+v", b)
	}
	if err := h.client.AckSpool(ctx, node.SpoolReplay, 1, 1); err != nil || fk.Acked(node.SpoolReplay) != 1 || fk.SpoolAcked() != 3 {
		t.Fatalf("replay ack: %v, acked %d and %d", err, fk.Acked(node.SpoolReplay), fk.SpoolAcked())
	}

	// The store runs from the control plane's binary, sent once.
	bin := filepath.Join(t.TempDir(), "dock-analytics")
	if err := os.WriteFile(bin, []byte("the store"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("the store"))
	spec := node.AnalyticsStoreSpec{Binary: bin, SHA256: hex.EncodeToString(sum[:]), Token: strings.Repeat("t", 32), MemoryBytes: 512 << 20}
	if err := h.client.EnsureAnalyticsStore(ctx, spec); err != nil {
		t.Fatal(err)
	}
	got := fk.AnalyticsStoreSpec()
	onNode := filepath.Join(h.dataDir, "analytics", "bin", "dock-analytics-"+spec.SHA256)
	if got == nil || got.Binary != onNode || got.SHA256 != spec.SHA256 || got.Token != spec.Token {
		t.Fatalf("the node runs %+v", got)
	}
	if b, err := os.ReadFile(onNode); err != nil || string(b) != "the store" {
		t.Fatalf("the node's copy: %q, %v", b, err)
	}
	// The next ensure finds it there: nothing is sent again.
	_ = os.Remove(bin)
	if err := h.client.EnsureAnalyticsStore(ctx, spec); err != nil {
		t.Fatalf("ensure with the binary on the node: %v", err)
	}
	// A binary that doesn't match its hash is refused.
	_ = os.WriteFile(bin, []byte("tampered"), 0o755)
	if err := h.client.EnsureAnalyticsStore(ctx, node.AnalyticsStoreSpec{Binary: bin, SHA256: strings.Repeat("a", 64), Token: spec.Token, MemoryBytes: spec.MemoryBytes}); err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Fatalf("a mismatched binary: %v", err)
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

	// Removing the store takes its binaries with it.
	if err := h.client.RemoveAnalyticsStore(ctx); err != nil || fk.AnalyticsStoreSpec() != nil {
		t.Fatalf("remove: %v, node has %+v", err, fk.AnalyticsStoreSpec())
	}
	if _, err := os.Stat(onNode); !os.IsNotExist(err) {
		t.Errorf("the binary stayed on the node: %v", err)
	}
}
