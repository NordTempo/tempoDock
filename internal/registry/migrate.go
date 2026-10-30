package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"golang.org/x/sync/errgroup"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// storageMount is where the registry container mounts StorageDir.
const storageMount = "/var/lib/registry"

// uploads is how many objects Migrate writes to a bucket at once.
const uploads = 8

// Migrate implements core.Registry: it moves the registry's images from
// where they are to to (nil: StorageDir) and switches the registry over.
//
//  1. The registry restarts read-only on its current storage: pulls keep
//     working, pushes and deletes are refused.
//  2. The target is prepared: a bucket must hold nothing under BucketRoot
//     (another Dock may use it) unless OwnerKey names owner, which makes
//     it a copy an interrupted migration of this install left, deleted
//     first; OwnerKey is then written. The storage dir is emptied (the
//     registry doesn't use it, so whatever is there is a stale copy).
//  3. Every object under docker/ is copied, except unfinished uploads,
//     and the target is listed to check it holds as many objects and
//     bytes as were copied.
//  4. commit records the new storage (core's setting), and the registry
//     restarts on it, writable again.
//
// Anything failing before commit puts the registry back as it was and
// removes the partial copy from a bucket target. The source is left as it
// is; Drop removes it once the caller is done. Disk to bucket reads the
// storage dir directly and uploads uploads objects at a time; bucket to
// disk streams a tar into the container (Docker's archive API), so the
// files belong to the registry's user. Bucket to bucket streams each
// object through the daemon.
func (p *Platform) Migrate(ctx context.Context, to *s3.Bucket, owner string, commit func() error) (objects int, size int64, err error) {
	from := p.current()
	if sameStorage(from, to) {
		return 0, 0, errors.New("the registry already keeps its images there")
	}
	if err := p.configure(ctx, Storage{Bucket: from, ReadOnly: true}); err != nil {
		return 0, 0, fmt.Errorf("make the registry read-only: %w", err)
	}
	committed, copying := false, false
	defer func() {
		if committed {
			return
		}
		// Back to where the images still are, taking pushes again.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		if copying && to != nil {
			if derr := p.Drop(ctx, to); derr != nil {
				err = errors.Join(err, fmt.Errorf("remove the partial copy: %w", derr))
			}
		}
		if rerr := p.configure(ctx, Storage{Bucket: from}); rerr != nil {
			err = errors.Join(err, fmt.Errorf("put the registry back: %w", rerr))
		}
	}()

	if to != nil {
		if err := prepareBucket(ctx, &s3.Client{Bucket: *to}, owner); err != nil {
			return 0, 0, err
		}
	} else if err := p.Drop(ctx, nil); err != nil {
		return 0, 0, fmt.Errorf("empty the storage dir: %w", err)
	}
	copying = true
	if objects, size, err = p.copyTo(ctx, from, to); err != nil {
		return 0, 0, err
	}
	gotObjects, gotSize, err := p.measure(ctx, to)
	if err != nil {
		return 0, 0, fmt.Errorf("check the copy: %w", err)
	}
	if gotObjects != objects || gotSize != size {
		return 0, 0, fmt.Errorf("the copy holds %d objects (%d bytes) instead of %d (%d bytes)", gotObjects, gotSize, objects, size)
	}
	if err := commit(); err != nil {
		return 0, 0, err
	}
	committed = true
	if err := p.UseStorage(ctx, to); err != nil {
		return objects, size, fmt.Errorf("the images were moved, but the registry didn't restart on them (restarting the daemon retries): %w", err)
	}
	return objects, size, nil
}

// OwnerKey holds the install ID of the Dock whose registry data is under
// BucketRoot in a bucket. Distribution ignores it (it reads only
// BucketRoot/docker/).
const OwnerKey = BucketRoot + "/dock-install"

// prepareBucket makes c's bucket ready for a migration's copy: empty
// under BucketRoot, or holding only what an earlier migration of owner
// left there, which is deleted. It then writes OwnerKey.
func prepareBucket(ctx context.Context, c *s3.Client, owner string) error {
	used, err := c.HasPrefix(ctx, BucketRoot+"/")
	if err != nil {
		return err
	}
	if used {
		mine := false
		if r, err := c.Get(ctx, OwnerKey); err == nil {
			b, _ := io.ReadAll(io.LimitReader(r, 256))
			_ = r.Close()
			mine = owner != "" && string(b) == owner
		} else if !s3.IsNotFound(err) {
			return err
		}
		if !mine {
			return fmt.Errorf("bucket %s already holds registry data under %s/, maybe another Dock's; empty that folder or use another bucket", c.Bucket.Name, BucketRoot)
		}
		// A copy an interrupted migration of this install left.
		if err := dropBucket(ctx, c); err != nil {
			return fmt.Errorf("remove an earlier partial copy: %w", err)
		}
	}
	return c.Put(ctx, OwnerKey, strings.NewReader(owner), int64(len(owner)))
}

// Drop implements core.Registry: it deletes the registry's data from
// storage (nil: StorageDir), which must not be the storage the registry
// uses. Migrate's caller drops the source once the move is recorded.
func (p *Platform) Drop(ctx context.Context, storage *s3.Bucket) error {
	if storage == nil {
		// The storage dir belongs to the registry's user: delete as it.
		return p.exec(ctx, "rm", "-rf", storageMount+"/docker")
	}
	return dropBucket(ctx, &s3.Client{Bucket: *storage})
}

// dropBucket deletes everything under BucketRoot in c's bucket, a page
// of up to 1000 objects at a time.
func dropBucket(ctx context.Context, c *s3.Client) error {
	return c.List(ctx, BucketRoot+"/", func(page []s3.Object) error {
		keys := make([]string, len(page))
		for i, o := range page {
			keys[i] = o.Key
		}
		return c.Delete(ctx, keys)
	})
}

// sameStorage reports whether a and b are the same storage.
func sameStorage(a, b *s3.Bucket) bool {
	if a == nil || b == nil {
		return a == b
	}
	return strings.TrimRight(a.Endpoint, "/") == strings.TrimRight(b.Endpoint, "/") && a.Name == b.Name
}

// object is one file of the registry's storage, relative to its root
// (docker/registry/v2/...).
type object struct {
	rel  string
	size int64
	open func(context.Context) (io.ReadCloser, error)
}

// walk calls fn with every object in storage (nil: StorageDir) under
// docker/, in path order, leaving out unfinished uploads.
func (p *Platform) walk(ctx context.Context, storage *s3.Bucket, fn func(object) error) error {
	if storage != nil {
		c := &s3.Client{Bucket: *storage}
		return c.List(ctx, BucketRoot+"/docker/", func(page []s3.Object) error {
			for _, o := range page {
				if strings.Contains(o.Key, "/_uploads/") {
					continue
				}
				key := o.Key
				err := fn(object{rel: strings.TrimPrefix(key, BucketRoot+"/"), size: o.Size, open: func(ctx context.Context) (io.ReadCloser, error) {
					return c.Get(ctx, key)
				}})
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	root := filepath.Join(p.StorageDir, "docker")
	err := filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "_uploads" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(p.StorageDir, file)
		if err != nil {
			return err
		}
		return fn(object{rel: filepath.ToSlash(rel), size: info.Size(), open: func(context.Context) (io.ReadCloser, error) {
			return os.Open(file)
		}})
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing pushed yet
	}
	return err
}

// measure counts the objects and bytes in storage (nil: StorageDir), as
// walk sees them.
func (p *Platform) measure(ctx context.Context, storage *s3.Bucket) (n int, size int64, err error) {
	err = p.walk(ctx, storage, func(o object) error {
		n++
		size += o.size
		return nil
	})
	return n, size, err
}

// copyTo copies every object from from to to and returns how many it
// copied and their size.
func (p *Platform) copyTo(ctx context.Context, from, to *s3.Bucket) (n int, size int64, err error) {
	if to == nil {
		return p.download(ctx, from)
	}
	dst := &s3.Client{Bucket: *to}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(uploads)
	err = p.walk(gctx, from, func(o object) error {
		n++
		size += o.size
		g.Go(func() error { return upload(gctx, dst, o) })
		return gctx.Err()
	})
	if werr := g.Wait(); werr != nil {
		return 0, 0, werr
	}
	return n, size, err
}

// upload copies one object into dst under BucketRoot, trying three times:
// one failed request out of thousands shouldn't fail a migration.
func upload(ctx context.Context, dst *s3.Client, o object) error {
	if o.size > s3.MaxPut {
		return fmt.Errorf("%s is %d bytes; a migration copies objects up to 5 GB", o.rel, o.size)
	}
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		var r io.ReadCloser
		if r, err = o.open(ctx); err != nil {
			continue
		}
		err = dst.Put(ctx, BucketRoot+"/"+o.rel, r, o.size)
		_ = r.Close()
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("copy %s: %w", o.rel, err)
}

// download copies the registry's objects from a bucket into the storage
// dir: one tar stream into the container, so the files and directories
// belong to the registry's user (CopyUIDGID). Directories are written
// before their first file; Docker would create missing ones as root.
func (p *Platform) download(ctx context.Context, from *s3.Bucket) (n int, size int64, err error) {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := p.Docker.CopyToContainer(ctx, p.container(), client.CopyToContainerOptions{
			DestinationPath: storageMount, Content: pr, CopyUIDGID: true,
		})
		_ = pr.CloseWithError(err)
		done <- err
	}()
	tw := tar.NewWriter(pw)
	dirs := map[string]bool{}
	mtime := time.Now()
	err = p.walk(ctx, from, func(o object) error {
		if err := mkdirs(tw, dirs, path.Dir(o.rel), mtime); err != nil {
			return err
		}
		r, err := o.open(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		if err := tw.WriteHeader(&tar.Header{Name: o.rel, Mode: 0o644, Size: o.size, ModTime: mtime, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := io.CopyN(tw, r, o.size); err != nil {
			return fmt.Errorf("copy %s: %w", o.rel, err)
		}
		n++
		size += o.size
		return nil
	})
	if err == nil {
		err = tw.Close()
	}
	_ = pw.CloseWithError(err)
	if derr := <-done; err == nil && derr != nil {
		err = fmt.Errorf("write into the registry's storage dir: %w", derr)
	}
	return n, size, err
}

// mkdirs writes a directory entry for dir and each of its parents not in
// seen yet.
func mkdirs(tw *tar.Writer, seen map[string]bool, dir string, mtime time.Time) error {
	if dir == "." || dir == "/" || seen[dir] {
		return nil
	}
	if err := mkdirs(tw, seen, path.Dir(dir), mtime); err != nil {
		return err
	}
	seen[dir] = true
	return tw.WriteHeader(&tar.Header{Name: dir + "/", Mode: 0o755, ModTime: mtime, Typeflag: tar.TypeDir})
}

// exec runs cmd in the registry container, as its user, and fails with
// its output if it exits non-zero.
func (p *Platform) exec(ctx context.Context, cmd ...string) error {
	created, err := p.Docker.ExecCreate(ctx, p.container(), client.ExecCreateOptions{Cmd: cmd, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return fmt.Errorf("%s in %s: %w", cmd[0], p.container(), err)
	}
	att, err := p.Docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("%s in %s: %w", cmd[0], p.container(), err)
	}
	var out bytes.Buffer
	_, _ = stdcopy.StdCopy(&out, &out, io.LimitReader(att.Reader, 64<<10))
	att.Close()
	res, err := p.Docker.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return fmt.Errorf("%s in %s: %w", cmd[0], p.container(), err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s in %s exited with %d: %s", cmd[0], p.container(), res.ExitCode, strings.TrimSpace(out.String()))
	}
	return nil
}
