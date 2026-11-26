package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/update"
)

// The node's side of the analytics store (ADR 0037): it runs the release's
// dock-analytics binary, which the control plane sends once per release
// (put-analytics-binary) when the node doesn't have it.

// errNoAnalyticsBinary is a node without the binary it was asked to run.
var errNoAnalyticsBinary = errors.New("this server doesn't have the analytics store's binary yet")

// analyticsBinaryPrefix names the received binaries: the prefix and the
// hash.
const analyticsBinaryPrefix = "dock-analytics-"

// analyticsBinDir holds the binaries the node received.
func (n *Node) analyticsBinDir() string {
	return filepath.Join(n.DataDir, "analytics", "bin")
}

// analyticsBinary is where the binary with hash sum is kept.
func (n *Node) analyticsBinary(sum string) string {
	return filepath.Join(n.analyticsBinDir(), analyticsBinaryPrefix+sum)
}

// analyticsStoreRoutes serves the store's calls that need the node's
// data directory, for the binaries.
func (n *Node) analyticsStoreRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/"+opRunAnalytics, unary(func(ctx context.Context, a analyticsStoreRequest) (empty, error) {
		return empty{}, n.ensureAnalyticsStore(ctx, a.Spec)
	}))
	// Protocol 10's call, from a control plane older than this node: it
	// ran the store from an image, which nothing publishes.
	mux.Handle("POST /v1/"+opEnsureAnalytics, unary(func(context.Context, json.RawMessage) (empty, error) {
		return empty{}, errors.New("this server runs the analytics store from the release's binary; update the control plane to its version")
	}))
	mux.Handle("POST /v1/"+opPutAnalyticsBinary, withLogs(func(_ context.Context, a putAnalyticsBinaryRequest, body io.Reader, _ io.Writer) (empty, error) {
		return empty{}, n.receiveAnalyticsBinary(a, body)
	}))
	mux.Handle("POST /v1/"+opRemoveAnalytics, unary(func(ctx context.Context, a storeKindRequest) (empty, error) {
		kind := node.StoreKind(a.Kind)
		if err := n.Runtime.RemoveAnalyticsStore(ctx, kind); err != nil || kind != node.StoreAnalytics {
			// The trace store runs the analytics store's binary: it stays.
			return empty{}, err
		}
		return empty{}, os.RemoveAll(n.analyticsBinDir())
	}))
	// The binary stays: the store runs on it again when turned on.
	mux.Handle("POST /v1/"+opStopAnalytics, unary(func(ctx context.Context, a storeKindRequest) (empty, error) {
		return empty{}, n.Runtime.StopAnalyticsStore(ctx, node.StoreKind(a.Kind))
	}))
}

// ensureAnalyticsStore runs the store from the binary with the spec's
// hash, then deletes the binaries it no longer runs.
func (n *Node) ensureAnalyticsStore(ctx context.Context, w wireAnalyticsStoreSpec) error {
	if !validSum(w.SHA256) {
		return fmt.Errorf("%q is not a SHA-256", w.SHA256)
	}
	bin := n.analyticsBinary(w.SHA256)
	if _, err := os.Stat(bin); errors.Is(err, fs.ErrNotExist) {
		return errNoAnalyticsBinary
	} else if err != nil {
		return err
	}
	if err := n.Runtime.EnsureAnalyticsStore(ctx, node.AnalyticsStoreSpec{Kind: node.StoreKind(w.Kind), Binary: bin, SHA256: w.SHA256, Token: w.Token, MemoryBytes: w.MemoryBytes}); err != nil {
		return err
	}
	entries, err := os.ReadDir(n.analyticsBinDir())
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(bin) {
			_ = os.Remove(filepath.Join(n.analyticsBinDir(), e.Name()))
		}
	}
	return nil
}

// receiveAnalyticsBinary writes the binary the control plane sends,
// checking its size and hash before it can be run.
func (n *Node) receiveAnalyticsBinary(a putAnalyticsBinaryRequest, body io.Reader) error {
	if !validSum(a.SHA256) || a.Size <= 0 || a.Size > update.MaxAsset {
		return fmt.Errorf("an analytics binary needs a SHA-256 and a size of at most %d MB", update.MaxAsset>>20)
	}
	if err := os.MkdirAll(n.analyticsBinDir(), 0o755); err != nil {
		return err
	}
	dst := n.analyticsBinary(a.SHA256)
	tmp := dst + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	h := sha256.New()
	got, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, a.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("receive the analytics binary: %w", err)
	case got != a.Size:
		return fmt.Errorf("the analytics binary is %d bytes, not %d", got, a.Size)
	case hex.EncodeToString(h.Sum(nil)) != a.SHA256:
		return errors.New("the analytics binary doesn't match its SHA-256")
	}
	return os.Rename(tmp, dst)
}

// validSum reports whether s is a hex SHA-256 (it names a file).
func validSum(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 64 && err == nil && strings.ToLower(s) == s
}
