package core

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// BenchmarkRetentionPass is the hourly retention pass on the seeded
// database (160 releases, about 2000 audit events) when nothing is due:
// the steady state. It reads the untrimmed releases through
// releases_history, one audit row and the resolved alerts' index.
func BenchmarkRetentionPass(b *testing.B) {
	bc := newBenchCore(b)
	now := time.Now()
	b.ReportAllocs()
	for b.Loop() {
		if got := bc.c.retain(bc.ctx, now); got != (retained{}) {
			b.Fatalf("trimmed %+v", got)
		}
	}
}

// BenchmarkAlertEvent is the alert engine reading one bus event that
// changes nothing (a service status without restarts): what every status
// and release event costs it.
func BenchmarkAlertEvent(b *testing.B) {
	bc := newBenchCore(b)
	ev := newLiveEvent(apitypes.Event{
		Type: apitypes.EventServiceStatus, Project: "big", Environment: "production", Service: "svc-00",
		ServiceStatus: &apitypes.ServiceStatus{State: apitypes.ServiceRunning, RestartCount: 1},
	})
	now := time.Now()
	bc.c.onAlertEvent(bc.ctx, ev, now)
	b.ReportAllocs()
	for b.Loop() {
		bc.c.onAlertEvent(bc.ctx, ev, now)
	}
}
