package main

import (
	"fmt"
	"time"
)

// peakSettle is how long a store's peak is still sampled after its ingest
// burst ends: DuckDB keeps growing for a few seconds afterwards (the
// checkpoint after the writes), which is part of what a burst costs.
const peakSettle = 10 * time.Second

// peakSampler polls a store's anonymous RSS every 100 ms while its check
// runs, for the analytics and trace stores' peak memory. VmHWM alone
// overstates what threatens the container: it counts the mapped binary
// and file pages, which the kernel reclaims before it kills anything
// (the containers have no swap, so anonymous memory is what can't be
// reclaimed). Polling stops with the check; nothing runs outside it.
type peakSampler struct {
	pid  int
	stop chan struct{}
	done chan int64
}

// samplePeak starts polling pid.
func samplePeak(pid int) *peakSampler {
	p := &peakSampler{pid: pid, stop: make(chan struct{}), done: make(chan int64, 1)}
	go func() {
		var peak int64
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			if kb, err := statusField(fmt.Sprintf("/proc/%d/status", pid), "RssAnon"); err == nil {
				peak = max(peak, kb<<10)
			}
			select {
			case <-p.stop:
				p.done <- peak
				return
			case <-tick.C:
			}
		}
	}()
	return p
}

// settle keeps sampling for peakSettle, then stops and returns the peak
// anonymous RSS and VmHWM, in bytes.
func (p *peakSampler) settle() (anon, hwm int64, err error) {
	time.Sleep(peakSettle)
	close(p.stop)
	anon = <-p.done
	kb, err := statusField(fmt.Sprintf("/proc/%d/status", p.pid), "VmHWM")
	return anon, kb << 10, err
}

// addPeak adds a store's peak memory row: anonymous memory against the
// container's limit, with VmHWM beside it.
func addPeak(t *table, name string, anon, hwm, budget int64) {
	t.within(name, fmt.Sprintf("%s anon (VmHWM %s)", mb(anon), mb(hwm)), "≤ "+mb(budget)+" (the container's limit)", anon <= budget)
}
