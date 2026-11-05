package core

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// BenchmarkJobPass is the job scheduler's pass over two jobs per service on the
// seeded project (in two timezones) when none is due:
// what every wake-up costs before it starts anything. One query and a
// cron step per job.
func BenchmarkJobPass(b *testing.B) {
	bc := newBenchCore(b)
	for i := range benchServices {
		ref := ServiceRef{"big", "production", fmt.Sprintf("svc-%02d", i)}
		for j, req := range []apitypes.SetJobRequest{
			{Schedule: "0 3 * * *", Timezone: "Europe/Oslo", Command: "bin/cleanup"},
			{Schedule: "*/15 9-17 * * mon-fri", Command: "bin/sweep"},
		} {
			if _, err := bc.c.SetJob(bc.ctx, testSystem, ref, fmt.Sprintf("job-%d", j), req); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if next, err := bc.c.jobPass(bc.ctx); err != nil || next.IsZero() {
			b.Fatalf("pass: %v, next %v", err, next)
		}
	}
}
