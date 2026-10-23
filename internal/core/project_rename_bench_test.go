package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// BenchmarkBusRenamedProject covers canonicalization of stale release events
// without mutating the release retained by the deployment goroutine.
func BenchmarkBusRenamedProject(b *testing.B) {
	bus := newBus()
	bus.renameProject(1, "storefront")
	sub := bus.subscribe(func(busEvent) bool { return true })
	defer sub.close()
	release := &apitypes.Release{Project: "shop", Environment: "production", Service: "web", Number: 1, Status: apitypes.ReleaseActive}
	event := busEvent{projectID: 1, ev: apitypes.Event{Type: apitypes.EventRelease, Project: "shop", Release: release}}
	b.ReportAllocs()
	for b.Loop() {
		bus.publish(event)
		if _, err := (<-sub.ch).JSON(); err != nil {
			b.Fatal(err)
		}
	}
	if release.Project != "shop" {
		b.Fatal("mutated deployment's release")
	}
}
