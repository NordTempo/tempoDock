package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

// Prune implements core.Registry, reading the links of tags the
// registry can't from storage (TagLink).
func (p *Platform) Prune(ctx context.Context, keep []string) (deleted int, skipped []error, err error) {
	res, err := p.Client.Prune(ctx, keep, p.TagLink)
	return len(res.Deleted), res.Skipped, err
}

// TagLink returns the digest repo:tag points at from the registry's
// storage (Distribution's tag link), without the registry: a
// registry.TagLink. In a bucket it reads the bucket directly, where an
// old release's link is (the gateway's spool only holds what waits to
// upload). The read gets as long as a registry call.
func (p *Platform) TagLink(ctx context.Context, repo, tag string) (string, error) {
	if !validRepo(repo) || !validTag.MatchString(tag) {
		return "", fmt.Errorf("not a repository and tag: %q:%q", repo, tag)
	}
	key := "docker/registry/v2/repositories/" + repo + "/_manifests/tags/" + tag + "/current/link"
	var body []byte
	var err error
	if b := p.current(); b != nil {
		ctx, cancel := context.WithTimeout(ctx, p.Client.timeout())
		defer cancel()
		var r io.ReadCloser
		if r, err = (&s3.Client{Bucket: *b}).Get(ctx, BucketRoot+"/"+key); err == nil {
			body, err = io.ReadAll(io.LimitReader(r, 1<<10))
			_ = r.Close()
		}
	} else {
		body, err = os.ReadFile(filepath.Join(p.StorageDir, filepath.FromSlash(key)))
	}
	if err != nil {
		return "", err
	}
	d := strings.TrimSpace(string(body))
	if !validDigest.MatchString(d) {
		return "", fmt.Errorf("the link of %s:%s holds %q, not a digest", repo, tag, d)
	}
	return d, nil
}

// validTag, validDigest and validPart match Distribution's grammar for
// tags, sha256 digests and a repository name's parts, so a name from the
// registry never makes a storage path outside its repositories.
var (
	validTag    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	validDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	validPart   = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
)

// validRepo reports whether repo is a repository name Distribution
// accepts: no empty or dot-only part can reach a parent directory.
func validRepo(repo string) bool {
	parts := strings.Split(repo, "/")
	for _, part := range parts {
		if !validPart.MatchString(part) {
			return false
		}
	}
	return len(repo) <= 255
}

// GarbageCollect implements core.Registry.
// While it runs, the gateway never reports a stuck blob missing: the
// collector would take it for unreferenced. It returns once the registry
// answers again, even when ctx ended it early (core stops a collection
// for a build that waits): the build pushes right after.
func (p *Platform) GarbageCollect(ctx context.Context) error {
	if p.Gateway != nil {
		p.Gateway.SetStrict(true)
		defer p.Gateway.SetStrict(false)
	}
	err := GarbageCollect(ctx, p.Docker, p.container(), p.current() != nil, p.Logs)
	if rerr := p.waitReady(context.WithoutCancel(ctx)); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

// Recover undoes what a daemon stopped in the middle of a garbage
// collection left behind. GarbageCollect stops the registry and starts it
// again when done; a crash or a kill skips that, leaving the registry
// stopped (every push fails) and the collector running (started again by
// hand, the registry would take pushes the collector may delete from).
// So the collector goes, killed midway it only leaves unreferenced blobs
// for the next one, and the registry starts. `dock serve` calls it on
// start, before anything can prune.
func (p *Platform) Recover(ctx context.Context) error {
	gc := p.container() + "-gc"
	if _, err := p.Docker.ContainerRemove(ctx, gc, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove the leftover %s: %w", gc, err)
	}
	inspect, err := p.Docker.ContainerInspect(ctx, p.container(), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil // install.sh creates it after the daemon starts
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", p.container(), err)
	}
	if inspect.Container.State.Running {
		return nil
	}
	if _, err := p.Docker.ContainerStart(ctx, p.container(), client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start %s: %w", p.container(), err)
	}
	return p.waitReady(ctx)
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
