package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"golang.org/x/sync/errgroup"
)

// warmMarker is the file, next to the work dir, naming the Railpack
// images WarmBuilder last fetched.
const warmMarker = "railpack-images"

// WarmBuilder implements node.Builder: it fetches the images Railpack
// builds run on (railpackImages, about 460 MB) into BuildKit's cache, so
// the first Railpack build on a new builder, and the first after an
// update that pins other images, doesn't wait for them: 20 s of a 62 s
// cold build on the dev VM, most of it the builder image (ADR 0089
// amendment).
//
// Core calls it for builders when they connect (the control plane's own
// at start); it does nothing when the marker says this set of images was
// fetched already, and one call at a time runs. It creates and starts
// the BuildKit container if needed, as the first build would, and leaves
// it running until the idle stop (idle.go). Builds don't wait for
// it: one that starts meanwhile shares the fetches. Images the build
// cache's pruning removes later are fetched by the next build that needs
// them, as before.
func (b *Builder) WarmBuilder(ctx context.Context) error {
	// Held even when there is nothing to fetch: its release arms the idle
	// stop, so a BuildKit left running by a daemon before this one stops.
	defer b.idle.hold(false)()
	b.warmMu.Lock()
	defer b.warmMu.Unlock()
	want := warmList()
	marker := filepath.Join(filepath.Dir(b.workDir), warmMarker)
	if got, err := os.ReadFile(marker); err == nil && string(got) == want {
		return nil
	}
	// The slot only for creating the container: a build that wants it
	// meanwhile would otherwise find it half made.
	select {
	case b.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	bk, err := b.ensureDaemon(ctx, io.Discard)
	<-b.sem
	if err != nil {
		return err
	}
	defer func() { _ = bk.Close() }()
	_, err = bk.Build(ctx, bkclient.SolveOpt{}, "dock", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		var g errgroup.Group
		for ref, digest := range railpackImages {
			g.Go(func() error { return fetchImage(ctx, c, ref+"@"+digest) })
		}
		return gateway.NewResult(), g.Wait()
	}, nil)
	if err != nil {
		return fmt.Errorf("fetch Railpack's images: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(want), 0o600)
}

// fetchImage pulls ref into BuildKit's cache. BuildKit fetches an
// image's layers only once something reads its files, so it stats the
// root: that unpacks the image as a build step would.
func fetchImage(ctx context.Context, c gateway.Client, ref string) error {
	def, err := llb.Image(ref).Marshal(ctx)
	if err != nil {
		return err
	}
	res, err := c.Solve(ctx, gateway.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	r, err := res.SingleRef()
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New(ref + ": no result")
	}
	if _, err := r.StatFile(ctx, gateway.StatRequest{Path: "/"}); err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	return nil
}

// warmList is the marker's content: the pinned references, one a line.
func warmList() string {
	var sb strings.Builder
	for _, ref := range slices.Sorted(maps.Keys(railpackImages)) {
		sb.WriteString(ref + "@" + railpackImages[ref] + "\n")
	}
	return sb.String()
}
