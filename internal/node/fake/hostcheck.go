package fake

import (
	"context"
	"slices"
	"sync"

	"github.com/NordTempo/tempoDock/internal/node"
)

// fakeHostChecks is the fake's node.HostChecker: scripted results, and
// how many times it was asked.
type fakeHostChecks struct {
	mu      sync.Mutex
	results []node.HostCheck
	err     error
	asked   int
}

// defaultHostChecks is what a fake host looks like until SetHostChecks:
// every check passes, so `make dev` shows a clean console.
var defaultHostChecks = []node.HostCheck{
	{Check: "ssh_password", Status: node.HostPass, Title: "SSH takes keys only"},
	{Check: "open_port", Status: node.HostPass, Title: "No port is open to the internet but SSH and Dock's"},
	{Check: "docker_tcp", Status: node.HostPass, Title: "Docker's API is not on TCP"},
}

// SetHostChecks sets what CheckHost answers from now on: res, or err.
func (r *Runtime) SetHostChecks(res []node.HostCheck, err error) {
	h := &r.hostChecks
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results, h.err = slices.Clone(res), err
}

// HostChecksAsked says how many times CheckHost was called.
func (r *Runtime) HostChecksAsked() int {
	h := &r.hostChecks
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.asked
}

// CheckHost implements node.HostChecker (SetHostChecks).
func (r *Runtime) CheckHost(context.Context) ([]node.HostCheck, error) {
	h := &r.hostChecks
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked++
	if h.err != nil {
		return nil, h.err
	}
	if h.results == nil {
		return slices.Clone(defaultHostChecks), nil
	}
	return slices.Clone(h.results), nil
}
