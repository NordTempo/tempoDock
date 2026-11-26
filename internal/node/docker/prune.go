package docker

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Which images a prune removes (ADR 0035 amendment 2). DiskUsage and
// PruneImages share imageRules, so what the console says a prune frees
// is what it removes.

// imageRules is a prune request made fast to ask, at one moment.
type imageRules struct {
	keep, unneeded, rollbacks map[string]bool
	repos                     []string
	// pinned are the digests of the platform's own images, by familiar
	// repository name ("moby/buildkit").
	pinned map[string][]string
	only   bool
	// before: an image made after it is too new to remove unasked.
	before int64
}

func (r *Runtime) imageRules(req node.PruneImagesRequest) imageRules {
	grace := r.PruneGrace
	if grace <= 0 {
		grace = time.Hour
	}
	set := func(ids []string) map[string]bool {
		m := make(map[string]bool, len(ids))
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	ru := imageRules{
		keep: set(req.Keep), unneeded: set(req.Unneeded), rollbacks: set(req.Rollbacks),
		repos: req.Repos, pinned: map[string][]string{}, only: req.OnlyRollbacks,
		before: time.Now().Add(-grace).Unix(),
	}
	for _, ref := range req.Pinned {
		named, err := reference.ParseNormalizedNamed(ref)
		if err != nil {
			continue
		}
		// Without a digest no version can be told apart: never prune it.
		if d, ok := named.(reference.Digested); ok {
			name := reference.FamiliarName(named)
			ru.pinned[name] = append(ru.pinned[name], d.Digest().String())
		}
	}
	return ru
}

// removable reports whether a prune of images (not of rollbacks) removes
// img, leaving aside whether a container uses it.
func (ru imageRules) removable(img image.Summary) bool {
	if ru.keep[img.ID] || img.Created > ru.before {
		return false
	}
	return ru.unneeded[img.ID] || ownedImage(img.Labels, img.RepoDigests, ru.repos) || ru.replaced(img)
}

// replaced reports whether img is another version of one of the
// platform's own images: a repository of one, without its digest.
func (ru imageRules) replaced(img image.Summary) bool {
	if len(ru.pinned) == 0 {
		return false
	}
	ours := false
	for _, name := range imageNames(img) {
		named, err := reference.ParseNormalizedNamed(name)
		if err != nil {
			continue
		}
		digests, ok := ru.pinned[reference.FamiliarName(named)]
		if !ok {
			continue
		}
		ours = true
		if d, ok := named.(reference.Digested); ok && slices.Contains(digests, d.Digest().String()) {
			return false // the version in use
		}
	}
	return ours
}

// imageNames are an image's tags and digest references, "<none>" left out.
func imageNames(img image.Summary) []string {
	var names []string
	for _, n := range slices.Concat(img.RepoTags, img.RepoDigests) {
		if !strings.HasPrefix(n, "<none>") {
			names = append(names, n)
		}
	}
	return names
}

// ownedImage reports whether the platform made an image with these labels
// and digest references (repo@sha256:...): it built it (node.LabelImage),
// or pulled it from a repository under one of repos (the platform
// registry's). An image it can't tell is never its own.
func ownedImage(labels map[string]string, repoDigests, repos []string) bool {
	if labels[node.LabelImage] == "true" {
		return true
	}
	for _, d := range repoDigests {
		repo, _, ok := strings.Cut(d, "@")
		if ok && slices.ContainsFunc(repos, func(p string) bool { return p != "" && strings.HasPrefix(repo, p) }) {
			return true
		}
	}
	return false
}

// PruneImages implements node.Containers. Docker's own `image prune`
// can't be told what to keep or whose images are whose, so this lists
// the images and removes those imageRules picks, each by its names one
// at a time (the last one removes the image; Docker refuses it while a
// container uses the image, which is never forced).
func (r *Runtime) PruneImages(ctx context.Context, req node.PruneImagesRequest) (node.PruneResult, error) {
	ru := r.imageRules(req)
	res, err := r.cli.ImageList(ctx, client.ImageListOptions{SharedSize: true})
	if err != nil {
		return node.PruneResult{}, fmt.Errorf("list images: %w", err)
	}
	used, err := r.usedImages(ctx)
	if err != nil {
		return node.PruneResult{}, err
	}
	var out node.PruneResult
	for _, img := range res.Items {
		if used[img.ID] || (ru.only && !ru.rollbacks[img.ID]) || (!ru.only && !ru.removable(img)) {
			continue
		}
		gone, err := r.removeImage(ctx, img)
		switch {
		case gone:
			out.Removed++
			out.ReclaimedBytes += max(img.Size-max(img.SharedSize, 0), 0)
		case ctx.Err() != nil:
			return out, ctx.Err()
		case err != nil:
			return out, err
		}
	}
	return out, nil
}

// usedImages are the IDs of the images containers use, stopped ones too.
func (r *Runtime) usedImages(ctx context.Context) (map[string]bool, error) {
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	used := make(map[string]bool, len(res.Items))
	for _, c := range res.Items {
		used[c.ImageID] = true
	}
	return used, nil
}

// removeImage removes an image by each of its names, or by its ID when
// it has none, and reports whether it is gone. An image a container came
// to use meanwhile, or that went already, is no error.
func (r *Runtime) removeImage(ctx context.Context, img image.Summary) (bool, error) {
	names := imageNames(img)
	if len(names) == 0 {
		names = []string{img.ID}
	}
	for _, name := range names {
		_, err := r.cli.ImageRemove(ctx, name, client.ImageRemoveOptions{PruneChildren: true})
		switch {
		case err == nil:
		case cerrdefs.IsConflict(err):
			return false, nil
		case cerrdefs.IsNotFound(err):
		default:
			return false, fmt.Errorf("remove image %s: %w", name, err)
		}
	}
	return true, nil
}
