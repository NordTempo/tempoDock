package core

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// BenchmarkAnalyticsWritten is what the forwarder adds per batch for the
// analytics alerts: a batch of 1,000 events, 10 of them errors, counted
// for spikes (ADR 0037). A batch without errors is one bytes.Contains.
func BenchmarkAnalyticsWritten(b *testing.B) {
	c := &Core{}
	// Spiking already, so counting raises nothing (no database here).
	c.analytics.alerts.rates = map[int64]*errorRate{3: {spiking: true}}
	t := now()
	var batch bytes.Buffer
	for i := range 1000 {
		if i%100 == 0 {
			batch.Write(spooledErrorLine(3, 40, 981, t, i))
		} else {
			fmt.Fprintf(&batch, `{"t":%d,"s":3,"sv":40,"rl":981,"h":"shop.example.com","k":1,"p":"/products/%d","v":%d,"b":"Chrome","o":"macOS","d":"desktop"}`, t.UnixMilli(), i, i)
		}
		batch.WriteByte('\n')
	}
	lines := batch.Bytes()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		c.analyticsWritten(ctx, analytics.WriteResult{}, lines)
	}
}
