package fake

import (
	"context"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// fakeProbes is the fake's node.Prober: scripted results by URL and the
// probes it was asked for.
type fakeProbes struct {
	mu      sync.Mutex
	results map[string]node.ProbeResult
	// asked lists the URLs probed, in order.
	asked []string
	// gate, when set, holds every probe until it is closed.
	gate chan struct{}
}

// SetProbe sets what probes of url see from now on. A URL without a
// result is up: 200 in 5 ms.
func (r *Runtime) SetProbe(url string, res node.ProbeResult) {
	p := &r.probes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.results == nil {
		p.results = map[string]node.ProbeResult{}
	}
	p.results[url] = res
}

// Probed returns the URLs probed so far, in order.
func (r *Runtime) Probed() []string {
	p := &r.probes
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.asked...)
}

// HoldProbes blocks probes until the returned func is called, for tests
// of a probe that is still running when its check is due again.
func (r *Runtime) HoldProbes() (release func()) {
	gate := make(chan struct{})
	p := &r.probes
	p.mu.Lock()
	p.gate = gate
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.gate = nil
			p.mu.Unlock()
			close(gate)
		})
	}
}

// Probe implements node.Prober with the result SetProbe set for the URL.
// Probes are recorded when they start.
func (r *Runtime) Probe(ctx context.Context, req node.ProbeRequest) (node.ProbeResult, error) {
	p := &r.probes
	p.mu.Lock()
	p.asked = append(p.asked, req.URL)
	res, ok := p.results[req.URL]
	gate := p.gate
	p.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return node.ProbeResult{Error: "no answer within " + req.Timeout.String(), Latency: req.Timeout}, nil
		}
	}
	if !ok {
		return node.ProbeResult{Up: true, Status: 200, Latency: 5 * time.Millisecond}, nil
	}
	return res, nil
}
