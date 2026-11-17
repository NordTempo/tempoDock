package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
)

// Nodes is the control plane's link to remote servers (ADR 0014): the
// node hub, adapted by NodesOf. Tests use a fake.
type Nodes interface {
	// Runtime returns an online server's runtime; ok is false while it is
	// offline or incompatible.
	Runtime(serverID int64) (node.Runtime, bool)
	// State is a server's link status and what it said in hello.
	State(serverID int64) remote.State
	// Events follows a server's container events across reconnects (see
	// remote.Client.Events and remote.ActionResync). The channel closes
	// when ctx ends.
	Events(ctx context.Context, serverID int64) (<-chan node.ContainerEvent, error)
	// Disconnect drops a server's live connection.
	Disconnect(serverID int64)
	// Rename has the link's messages and logs use a server's new name.
	Rename(serverID int64, name string)
	// RotateKey has an online server switch to a new key.
	RotateKey(ctx context.Context, serverID int64) (string, error)
	// MintJoinToken creates a one-time join token.
	MintJoinToken() (remote.JoinToken, error)
	// WaitOnline waits until a server is online, or ctx ends.
	WaitOnline(ctx context.Context, serverID int64) error
	// UpdateNode sends a server a release and waits for it to come back
	// on it (ADR 0028).
	UpdateNode(ctx context.Context, serverID int64, u remote.NodeUpdate) (remote.Hello, error)
}

// NodesOf adapts the node hub to Nodes.
//
// A nil hub gives a nil Nodes, never one wrapping nil: c.nodes != nil is
// how Core tells that remote servers exist, and a wrapped nil hub would
// panic on the first runtime lookup.
func NodesOf(h *remote.Hub) Nodes {
	if h == nil {
		return nil
	}
	return hubNodes{h}
}

type hubNodes struct{ *remote.Hub }

func (h hubNodes) Events(ctx context.Context, serverID int64) (<-chan node.ContainerEvent, error) {
	return h.Client(serverID).Events(ctx)
}

// SetNodes plugs in the node hub. Call it once, after New and before
// Start; the hub's HubOptions take NodeStore and NodeStatus from this
// Core, so it can only be created after New:
//
//	c, _ := core.New(ctx, opts)
//	hub := remote.NewHub(remote.HubOptions{Store: c.NodeStore(), OnStatus: c.NodeStatus, ...})
//	c.SetNodes(core.NodesOf(hub))
//	c.Start(ctx)
//	go hub.Serve(ctx, ln)
func (c *Core) SetNodes(n Nodes) { c.nodes = n }
