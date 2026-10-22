package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node/remote"
)

// serverState is what core keeps in memory per server.
type serverState struct {
	// build allows one build at a time on a builder.
	build chan struct{}
}

// imageUpkeep is registry and image maintenance state (images.go).
type imageUpkeep struct{}

// buildLock is a server's build semaphore: one build at a time per
// builder.
func (c *Core) buildLock(serverID int64) chan struct{} {
	c.serversMu.Lock()
	defer c.serversMu.Unlock()
	st := c.servers[serverID]
	if st == nil {
		st = &serverState{}
		c.servers[serverID] = st
	}
	if st.build == nil {
		st.build = make(chan struct{}, 1)
	}
	return st.build
}

// NodeStore is the hub's view of the servers and join_tokens tables, for
// remote.HubOptions.Store.
func (c *Core) NodeStore() remote.Store { return nodeStore{c} }

type nodeStore struct{ c *Core }

func (s nodeStore) NodeKey(ctx context.Context, fingerprint string) (remote.NodeKey, bool, error) {
	return remote.NodeKey{}, false, nil
}

func (s nodeStore) Join(ctx context.Context, req remote.JoinRequest) (remote.Server, error) {
	return remote.Server{}, remote.ErrJoinRejected
}

func (s nodeStore) SetNextKey(ctx context.Context, serverID int64, fingerprint string) error {
	return nil
}

func (s nodeStore) PromoteKey(ctx context.Context, serverID int64) error { return nil }

// NodeStatus is remote.HubOptions.OnStatus: it records a remote server's
// link going up (with its hello facts) or down, publishes the change and,
// on every connect, reconciles that server in the background.
func (c *Core) NodeStatus(s remote.State) {}

// AddServer starts adding a server (`dock server add`): it mints a
// one-time join token for it, valid for remote.JoinTokenTTL, and returns
// the token (shown only this once) and the install command to run on the
// new server. The server shows as joining until it joins. Admin only.
func (c *Core) AddServer(ctx context.Context, a Actor, req apitypes.AddServerRequest) (apitypes.AddedServer, error) {
	return apitypes.AddedServer{}, errf(apitypes.CodeInternal, "not implemented yet")
}

// RevokeServer forgets a remote server's key and drops its connection:
// it can't connect again until it re-joins with a new token. Its
// environments stay placed there, and their apps keep running on that
// machine. Admin only.
func (c *Core) RevokeServer(ctx context.Context, a Actor, name string) (apitypes.Server, error) {
	return apitypes.Server{}, errf(apitypes.CodeInternal, "not implemented yet")
}

// RotateServerKey has an online remote server switch to a new link key.
// Admin only.
func (c *Core) RotateServerKey(ctx context.Context, a Actor, name string) (apitypes.Server, error) {
	return apitypes.Server{}, errf(apitypes.CodeInternal, "not implemented yet")
}

// RemoveServer removes a remote server that has no environments, or
// cancels the join tokens of a server that hasn't joined yet. Admin only.
func (c *Core) RemoveServer(ctx context.Context, a Actor, name string) error {
	return errf(apitypes.CodeInternal, "not implemented yet")
}
