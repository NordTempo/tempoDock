package registry

import (
	"context"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/moby/moby/client"
)

// Platform is the registry as the control plane's core uses it
// (core.Registry): token minting, digests, pruning and garbage
// collection in one value. dock serve builds it when [registry] is
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
	// storage_dir), which Stored measures.
	StorageDir string
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
	name := p.Container
	if name == "" {
		name = ContainerName
	}
	return GarbageCollect(ctx, p.Docker, name, p.Logs)
}

// Stored implements core.Registry: the size of the registry's blobs, the
// files under StorageDir's blob store (Distribution's filesystem driver).
// Manifests are blobs too, and the repositories tree holds only links, so
// this is what the registry takes on disk. One walk, no cache: the console
// asks when an admin opens it.
func (p *Platform) Stored() (int64, error) {
	var n int64
	root := filepath.Join(p.StorageDir, "docker", "registry", "v2", "blobs")
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
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
