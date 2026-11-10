package core

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/store"
)

// BenchmarkSleepPass50 is the minute tick's sleep step with 50 sleepable
// environments, each with a service that had requests and none due to
// sleep: the common case, in memory only (no query, no runtime call).
func BenchmarkSleepPass50(b *testing.B) {
	c := &Core{localID: 1}
	c.sleep.envs = map[int64]*sleepEnv{}
	active := map[int64]*serviceTraffic{}
	now := time.Now()
	for i := range int64(50) {
		c.sleep.envs[i] = &sleepEnv{serverID: 1, after: 30 * time.Minute, lastActive: now}
		active[100+i] = &serviceTraffic{host: store.ListTrafficHostsRow{EnvironmentID: i}}
	}
	b.ReportAllocs()
	for b.Loop() {
		c.sleepTick(now, active)
	}
}
