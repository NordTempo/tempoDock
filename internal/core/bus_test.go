package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Subscribers share each event's JSON, and a reader gets everything queued
// in one batch, led by a lagged event where events were dropped.
func TestBusSharesEncodingAndDrainsBatches(t *testing.T) {
	b := newBus()
	all := func(busEvent) bool { return true }
	s1, s2 := b.subscribe(all), b.subscribe(all)
	defer s1.close()
	defer s2.close()

	b.publish(busEvent{ev: apitypes.Event{Type: apitypes.EventAudit}})
	j1, err := (<-s1.ch).JSON()
	if err != nil {
		t.Fatal(err)
	}
	j2, _ := (<-s2.ch).JSON()
	if &j1[0] != &j2[0] {
		t.Fatal("each subscriber encoded the event")
	}

	for range subscriptionBuffer + 1 {
		b.publish(busEvent{ev: apitypes.Event{Type: apitypes.EventRelease}})
	}
	batch := s1.drain(<-s1.ch)
	if len(batch) != subscriptionBuffer+1 || batch[0].Type != apitypes.EventLagged || batch[1].Type != apitypes.EventRelease {
		t.Fatalf("batch of %d, first %s", len(batch), batch[0].Type)
	}
	if len(s1.ch) != 0 || s1.takeLagged() {
		t.Fatal("drain left events or the lagged flag behind")
	}
}
