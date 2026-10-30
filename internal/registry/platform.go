package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

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
func (p *Platform) GarbageCollect(ctx context.Context) error {
	return GarbageCollect(ctx, p.Docker, p.container(), p.current() != nil, p.Logs)
}

// UseStorage implements core.Registry: it points the registry at bucket
// (nil: StorageDir), rewriting its config and restarting it if that
// changed. The daemon calls it on start, so the config always follows the
// setting, including after a crash in the middle of Migrate.
func (p *Platform) UseStorage(ctx context.Context, bucket *s3.Bucket) error {
	p.mu.Lock()
	p.bucket = bucket
	p.mu.Unlock()
	return p.configure(ctx, Storage{Bucket: bucket})
}

// configure writes the registry's config for st and restarts the
// registry if it changed and the container exists (install.sh creates it
// after the daemon starts).
func (p *Platform) configure(ctx context.Context, st Storage) error {
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
