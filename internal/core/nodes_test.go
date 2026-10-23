package core

import (
	"context"
	"strings"
	"sync"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/node/remote"
)

// fakeNodes stands in for the node hub: remote servers are fake runtimes
// that tests take offline and back.
type fakeNodes struct {
	mu    sync.Mutex
	rts   map[int64]*fake.Runtime
	off   map[int64]bool
	hello map[int64]remote.Hello
	// disconnects counts Disconnect calls per server.
	disconnects map[int64]int
	minted      int
	// updated lists UpdateNode calls by server; updateErr fails one.
	updated   map[int64][]remote.NodeUpdate
	updateErr map[int64]error
}

func newFakeNodes() *fakeNodes {
	return &fakeNodes{
		rts: map[int64]*fake.Runtime{}, off: map[int64]bool{}, hello: map[int64]remote.Hello{},
		disconnects: map[int64]int{}, updated: map[int64][]remote.NodeUpdate{}, updateErr: map[int64]error{},
	}
}

// add registers a remote server's runtime, online.
func (n *fakeNodes) add(id int64, rt *fake.Runtime, hello remote.Hello) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rts[id], n.hello[id] = rt, hello
}

func (n *fakeNodes) setOffline(id int64, off bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.off[id] = off
}

func (n *fakeNodes) Runtime(id int64) (node.Runtime, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if rt, ok := n.rts[id]; ok && !n.off[id] {
		return rt, true
	}
	return nil, false
}

func (n *fakeNodes) State(id int64) remote.State {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.rts[id]; ok && !n.off[id] {
		return remote.State{ServerID: id, Status: remote.StatusOnline, Hello: n.hello[id]}
	}
	return remote.State{ServerID: id, Status: remote.StatusOffline}
}

func (n *fakeNodes) Events(ctx context.Context, id int64) (<-chan node.ContainerEvent, error) {
	n.mu.Lock()
	rt := n.rts[id]
	n.mu.Unlock()
	if rt == nil {
		ch := make(chan node.ContainerEvent)
		context.AfterFunc(ctx, func() { close(ch) })
		return ch, nil
	}
	return rt.Events(ctx)
}

func (n *fakeNodes) Disconnect(id int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.off[id] = true
	n.disconnects[id]++
}

func (n *fakeNodes) RotateKey(context.Context, int64) (string, error) {
	return "sha256:" + strings.Repeat("ab", 32), nil
}

func (n *fakeNodes) MintJoinToken() (remote.JoinToken, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.minted++
	return remote.JoinToken{Addrs: []string{"100.64.0.1:7443"}, ControlPlane: "sha256:" + strings.Repeat("cd", 32), Secret: [32]byte{byte(n.minted), 2, 3}}, nil
}

// WaitOnline fails at once for an offline server: tests don't wait.
func (n *fakeNodes) WaitOnline(_ context.Context, id int64) error {
	if n.State(id).Status != remote.StatusOnline {
		return remote.ErrOffline
	}
	return nil
}

// UpdateNode records the update and, unless updateErr fails it, has the
// server come back on its version.
func (n *fakeNodes) UpdateNode(_ context.Context, id int64, u remote.NodeUpdate) (remote.Hello, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.updated[id] = append(n.updated[id], u)
	if err := n.updateErr[id]; err != nil {
		return n.hello[id], err
	}
	h := n.hello[id]
	h.Version = u.Version
	n.hello[id] = h
	return h, nil
}
