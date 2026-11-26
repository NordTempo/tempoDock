package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/version"
)

// UpdateProtocol is the first node protocol with the update call (ADR
// 0028). Older nodes are updated by hand.
const UpdateProtocol = 4

// SignedUpdateProtocol is the first that takes a release's SigAsset with
// its update (ADR 0088); older nodes refuse a file they don't expect, and
// don't check signatures anyway.
const SignedUpdateProtocol = 30

// CaddyUpdateProtocol is the first that takes the release's Dock Caddy
// (update.CaddyAsset, ADR 0106) with its update; older nodes refuse it,
// and their install.sh fetches it from the release itself.
const CaddyUpdateProtocol = 36

// updateTimeout bounds sending a release to a node: three or four files, ~40 MB.
const updateTimeout = 10 * time.Minute

// ErrNoUpdateCall is a node too old to be updated over its link.
var ErrNoUpdateCall = errors.New("it can't be updated over its link; run install.sh on it once")

// NodeUpdate is a release to send to a node: staged in Dir
// (update.ReleaseDir) and listed in Sums.
type NodeUpdate struct {
	Version     string
	RequestedBy string
	Dir         string
	Sums        update.Sums
}

// UpdateNode updates a server to u (ADR 0028): it waits for the server to
// be online, streams the release for its arch over the link, and waits
// for it to reconnect after its updater restarted it. It returns the new
// hello; a node that comes back on another version rolled back. ctx
// bounds the whole wait.
func (h *Hub) UpdateNode(ctx context.Context, serverID int64, u NodeUpdate) (Hello, error) {
	s, err := h.wait(ctx, serverID)
	if err != nil {
		return Hello{}, offline(ctx, h.name(serverID), err)
	}
	if s.hello.Protocol < UpdateProtocol || s.hello.Arch == "" {
		return Hello{}, fmt.Errorf("server %s runs dock %s: %w", s.name(), s.hello.Version, ErrNoUpdateCall)
	}
	req := updateRequest{Version: u.Version, RequestedBy: u.RequestedBy}
	var files []*os.File
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	names := update.Assets(u.Version, s.hello.Arch)
	if s.hello.Protocol >= SignedUpdateProtocol && update.Exists(u.Dir, update.SigAsset) {
		names = slices.Insert(names, 1, update.SigAsset)
	}
	if _, ok := u.Sums[update.CaddyAsset(s.hello.Arch)]; ok && s.hello.Protocol >= CaddyUpdateProtocol {
		names = append(names, update.CaddyAsset(s.hello.Arch))
	}
	readers := make([]io.Reader, 0, len(names))
	for _, name := range names {
		path := filepath.Join(u.Dir, name)
		sum, ok := u.Sums[name]
		if name == update.SumsAsset || name == update.SigAsset {
			// SHA256SUMS doesn't list itself or its signature; the node
			// checks the copies it gets are the ones the control plane
			// verified, and its updater checks the signature again.
			sum, err = update.HashFile(path)
			ok = err == nil
		}
		if !ok {
			return Hello{}, fmt.Errorf("release %s has no %s for server %s", u.Version, name, s.name())
		}
		f, err := os.Open(path)
		if err != nil {
			return Hello{}, err
		}
		files = append(files, f)
		st, err := f.Stat()
		if err != nil {
			return Hello{}, err
		}
		req.Files = append(req.Files, updateFile{Name: name, Size: st.Size(), SHA256: sum})
		readers = append(readers, f)
	}
	callCtx, cancel := context.WithTimeout(ctx, updateTimeout)
	err = call(callCtx, s, opUpdate, req, io.MultiReader(readers...), nil, nil, &empty{})
	cancel()
	if err != nil {
		return Hello{}, fmt.Errorf("send %s to server %s: %w", u.Version, s.name(), err)
	}
	// Its updater restarts it: this session ends, and the next one says
	// which version it runs.
	select {
	case <-s.done:
	case <-ctx.Done():
		return Hello{}, fmt.Errorf("server %s didn't restart for its update (see journalctl -u dock-update on it): %w", s.name(), ctx.Err())
	}
	next, err := h.waitNext(ctx, serverID, s)
	if err != nil {
		return Hello{}, fmt.Errorf("server %s didn't come back after its update: %w", s.name(), err)
	}
	if next.hello.Version != u.Version {
		return next.hello, fmt.Errorf("server %s came back on %s: its update failed and was rolled back; see journalctl -u dock-update on it", s.name(), next.hello.Version)
	}
	return next.hello, nil
}

// receiveUpdate is the node's side of the update call: it checks the
// files while writing them to its update directory, then asks its
// updater to apply them.
func (n *Node) receiveUpdate(_ context.Context, req updateRequest, body io.Reader) (empty, error) {
	if n.UpdaterUnit == "" {
		return empty{}, errors.New("this node takes no updates over its link")
	}
	if _, err := os.Stat(n.UpdaterUnit); err != nil {
		return empty{}, fmt.Errorf("this server has no updater (%s): update it by hand once, with install.sh from the control plane's release", filepath.Base(n.UpdaterUnit))
	}
	dir := update.Dir(n.DataDir)
	if update.Exists(dir, update.RequestFile) || update.Exists(dir, update.ApplyingFile) {
		return empty{}, errors.New("an update is already running on this server")
	}
	files := make([]update.File, len(req.Files))
	for i, f := range req.Files {
		files[i] = update.File{Name: f.Name, Size: f.Size, SHA256: f.SHA256}
	}
	if err := update.Receive(n.DataDir, req.Version, runtime.GOARCH, files, body); err != nil {
		return empty{}, err
	}
	n.logger().Info("received an update; the updater takes over", "version", req.Version, "requested_by", req.RequestedBy)
	return empty{}, update.WriteJSON(dir, update.RequestFile, update.Request{
		Version: req.Version, RequestedBy: req.RequestedBy, RequestedAt: time.Now().UTC(),
	}, nil)
}

// markConnected records, once per process, that this node said hello to
// its control plane: the node updater's health check.
func (n *Node) markConnected() {
	n.connectedOnce.Do(func() {
		dir := update.Dir(n.DataDir)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			err = update.WriteJSON(dir, update.ConnectedFile, update.Connected{Version: version.Version, At: time.Now().UTC()}, nil)
			if err != nil {
				n.logger().Warn("record the connection for the updater", "err", err)
			}
		}
	})
}

// waitNext waits for an online session of the server other than old,
// which may still be registered for a moment after it closed.
func (h *Hub) waitNext(ctx context.Context, serverID int64, old *session) (*session, error) {
	for {
		h.mu.Lock()
		s, changed := h.sessions[serverID], h.changed
		h.mu.Unlock()
		if s != nil && s != old && s.status == StatusOnline {
			return s, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
