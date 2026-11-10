package remote

import (
	"context"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Sleeping environments over the link (ADR 0070): a node's wake request
// reaches the control plane on its event stream, and resume and
// open-requests run on the node.
func TestSleepOverTheLink(t *testing.T) {
	h := newHarness(t)
	ct := h.start("web-1")
	ctx := context.Background()
	events, err := h.client.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.emitUntilReceived(events, ct.ID) // subscribed

	if !h.node.RequestWake("42", "GET /orders/:id") {
		t.Fatal("RequestWake with a stream open = false")
	}
	select {
	case ev := <-events:
		if ev.Action != node.ActionWake || ev.Labels.Environment != "42" || ev.Request != "GET /orders/:id" {
			t.Fatalf("wake event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wake request never arrived")
	}

	if err := h.client.Stop(ctx, ct.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Resume(ctx, ct.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := h.client.Inspect(ctx, ct.ID); err != nil || got.State != node.StateRunning || got.ID != ct.ID {
		t.Fatalf("after resume: %+v, %v", got, err)
	}
	h.rt.SetOpenRequests(2)
	if n, err := h.client.OpenRequests(ctx, []string{"127.0.0.1:41000"}); err != nil || n != 2 {
		t.Fatalf("open requests = %d, %v", n, err)
	}
}
