package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/registry/gateway"
	"github.com/NordTempo/tempoDock/internal/s3"
)

// Platform is the registry as the control plane's core uses it
// (core.Registry): token minting, digests, pruning, garbage collection
// and its storage in one value. dock serve builds it when [registry] is
// enabled.
type Platform struct {
	// Name is the registry's host:port in image references, the same on
	// every server ([registry] public_name, 127.0.0.1:5000).
	Name   string
	Signer *Signer
	// Client reaches the registry API from the control plane.
	Client *Client
	// Docker runs GarbageCollect's collector next to the registry
	// container (Container, default ContainerName).
	Docker    *client.Client
	Container string
	// Logs receives the collector's output; nil discards it.
	Logs io.Writer
	// StorageDir is the registry's storage on this host ([registry]
	// storage_dir): what Stored measures and Migrate copies when the
	// images are on disk.
	StorageDir string
	// ConfigDir is the registry's config dir on this host ([registry]
	// config_dir), which UseStorage and Migrate rewrite.
	ConfigDir string
	// Listen is [registry] listen: where the registry listens on the
	// host's network (ADR 0084).
	Listen string
	// Gateway is the storage gateway in front of a bucket, served on
	// GatewayAddr ([registry] gateway_listen); nil without one (tests).
	Gateway     *gateway.Gateway
	GatewayAddr string

	mu sync.Mutex
	// bucket is the storage UseStorage last set; nil is StorageDir.
	bucket *s3.Bucket
}

// Host implements core.Registry.
func (p *Platform) Host() string { return p.Name }

// Token implements core.Registry: a pull token valid TokenTTL, or a push
// (and pull) token valid PushTokenTTL, for one repository.
func (p *Platform) Token(subject, repo string, push bool) string {
	if push {
		return p.Signer.Mint(subject, PushTokenTTL, Push(repo))
	}
	return p.Signer.Mint(subject, TokenTTL, Pull(repo))
}

// Digest implements core.Registry.
func (p *Platform) Digest(ctx context.Context, repo, tag string) (string, error) {
	return p.Client.Digest(ctx, repo, tag)
}

// Prune implements core.Registry.
func (p *Platform) Prune(ctx context.Context, keep []string) (int, error) {
	deleted, err := p.Client.Prune(ctx, keep)
	return len(deleted), err
}

// GarbageCollect implements core.Registry.
// While it runs, the gateway never reports a stuck blob missing: the
// collector would take it for unreferenced.
func (p *Platform) GarbageCollect(ctx context.Context) error {
	if p.Gateway != nil {
		p.Gateway.SetStrict(true)
		defer p.Gateway.SetStrict(false)
	}
	return GarbageCollect(ctx, p.Docker, p.container(), p.current() != nil, p.Logs)
}

// PushStarted implements core.Registry: a push runs until done (the
// gateway's stuck blobs are reported missing meanwhile, ADR 0084).
func (p *Platform) PushStarted() (done func()) {
	if p.Gateway == nil {
		return func() {}
	}
	return p.Gateway.PushStarted()
}

// UseStorage implements core.Registry: it points the registry at bucket
// (nil: StorageDir), rewriting its config and restarting it if that
// changed. The daemon calls it on start, so the config always follows the
// setting, including after a crash in the middle of Migrate.
func (p *Platform) UseStorage(ctx context.Context, bucket *s3.Bucket) error {
	p.mu.Lock()
	p.bucket = bucket
	p.mu.Unlock()
	if p.Gateway != nil {
		p.Gateway.SetBucket(bucket)
	}
	return p.configure(ctx, Storage{Bucket: bucket})
}

// configure writes the registry's config for st and restarts the
// registry if it changed and the container exists (install.sh creates it
// after the daemon starts).
//
// A registry container on the host's network (install.sh since ADR 0084,
// or none yet) listens on Listen and reaches a bucket through the
// gateway. One from before, on Docker's bridge network, keeps the old
// config until install.sh recreates it (`dock admin update` does):
// pointed at the host's loopback it couldn't reach the gateway.
func (p *Platform) configure(ctx context.Context, st Storage) error {
	host, err := p.hostNetwork(ctx)
	if err != nil {
		return err
	}
	if host {
		st.Listen = p.Listen
		if p.Gateway != nil {
			st.Gateway = p.GatewayAddr
		}
	}
	changed, err := WriteConfig(p.ConfigDir, p.Signer, st)
	if err != nil || !changed {
		return err
	}
	timeout := 30
	_, err = p.Docker.ContainerRestart(ctx, p.container(), client.ContainerRestartOptions{Timeout: &timeout})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("restart the registry: %w", err)
	}
	return p.waitReady(ctx)
}

// waitReady waits up to a minute for the registry to answer after a
// restart, so the next pull doesn't fail while it starts. It asks every
// 100 ms; a restart takes about a second.
func (p *Platform) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Client.URL+"/v2/", nil)
		if err != nil {
			return err
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			return nil // 401 is an answer: the registry wants a token
		}
		select {
		case <-ctx.Done():
			return errors.New("the registry didn't come back within a minute after its restart; see `docker logs " + p.container() + "`")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Bucket implements core.Registry: the bucket's health and the spool
// (ADR 0084), nil when the images are on disk.
func (p *Platform) Bucket() *apitypes.RegistryBucket {
	if p.Gateway == nil || p.current() == nil {
		return nil
	}
	return BucketStatus(p.Gateway.Status())
}

// BucketStatus is st as the API shows it.
func BucketStatus(st gateway.Status) *apitypes.RegistryBucket {
	out := &apitypes.RegistryBucket{
		State: st.State, Since: st.Since.UTC(), Calls: st.Calls, Bad: st.Bad, SlowestMS: st.Slowest.Milliseconds(), LastError: st.LastError,
		Waiting: st.Waiting, WaitingBytes: st.WaitingBytes, Uploading: st.Uploading,
		SpoolBytes: st.SpoolBytes, SpoolMaxBytes: st.SpoolMax, DiskFreeBytes: st.DiskFree, DiskBytes: st.DiskTotal,
		DiskMinFreePercent: int(st.MinFree*100 + 0.5), Refusing: st.Refusing, Stuck: []apitypes.RegistryStuckBlob{},
	}
	if !st.OldestWaiting.IsZero() {
		t := st.OldestWaiting.UTC()
		out.OldestWaitingAt = &t
	}
	if !st.RefusingSince.IsZero() {
		t := st.RefusingSince.UTC()
		out.RefusingSince = &t
	}
	seen := map[string]bool{} // a blob and its layer link are one
	for _, b := range st.Stuck {
		if d := blobDigest(b.Key); !seen[d] {
			seen[d] = true
			out.Stuck = append(out.Stuck, apitypes.RegistryStuckBlob{Digest: d, Since: b.Since.UTC()})
		}
	}
	return out
}

// blobDigest is the digest of a blob's key in Distribution's blob store
// (.../blobs/sha256/e3/e3a8.../data) or of a layer link
// (.../_layers/sha256/e3a8.../link).
func blobDigest(key string) string {
	parts := strings.Split(key, "/")
	switch n := len(parts); {
	case n >= 4 && parts[n-1] == "data":
		return parts[n-4] + ":" + parts[n-2]
	case n >= 3 && parts[n-1] == "link":
		return parts[n-3] + ":" + parts[n-2]
	}
	return key
}

// hostNetwork reports whether the registry container runs on the host's
// network, or doesn't exist yet (install.sh creates it so).
func (p *Platform) hostNetwork(ctx context.Context) (bool, error) {
	if p.Docker == nil {
		return true, nil
	}
	c, err := p.Docker.ContainerInspect(ctx, p.container(), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect the registry: %w", err)
	}
	h := c.Container.HostConfig
	return h != nil && h.NetworkMode.IsHost(), nil
}

// container is the registry container's name.
func (p *Platform) container() string {
	if p.Container == "" {
		return ContainerName
	}
	return p.Container
}

// current is the bucket the registry uses, nil for StorageDir.
func (p *Platform) current() *s3.Bucket {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bucket
}

// Stored implements core.Registry: the size of the registry's blobs.
// Manifests are blobs too, and the repositories tree holds only links, so
// this is what the registry takes. On disk it walks the blob store under
// StorageDir (Distribution's filesystem driver); in a bucket it pages
// through the listing of the same tree, 1000 objects a request. No cache:
// the console asks when an admin opens it.
func (p *Platform) Stored(ctx context.Context) (int64, error) {
	var n int64
	if b := p.current(); b != nil {
		c := &s3.Client{Bucket: *b}
		err := c.List(ctx, BucketRoot+"/"+blobsPath, func(page []s3.Object) error {
			for _, o := range page {
				n += o.Size
			}
			return nil
		})
		return n, err
	}
	err := filepath.WalkDir(filepath.Join(p.StorageDir, filepath.FromSlash(blobsPath)), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err == nil {
			n += info.Size()
		}
		return err
	})
	return n, err
}

// blobsPath is Distribution's blob store, relative to its root.
const blobsPath = "docker/registry/v2/blobs/"
