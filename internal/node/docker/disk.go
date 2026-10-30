package docker

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"time"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// DiskUsage implements node.Containers with Docker's own disk usage
// (`docker system df -v`), which sizes every image and volume, and a walk
// of DataDir. An image is reclaimable when PruneImages would remove it:
// dangling, made by the platform, unused and not in req.Keep.
func (r *Runtime) DiskUsage(ctx context.Context, req node.PruneImagesRequest) (node.DiskUsage, error) {
	res, err := r.systemDF(ctx)
	if err != nil {
		return node.DiskUsage{}, err
	}
	var out node.DiskUsage
	var unique int64
	for _, img := range res.Images.Items {
		own := max(img.Size-max(img.SharedSize, 0), 0)
		unique += own
		switch {
		case img.Containers > 0:
			out.ImagesRunning += own
		case !ownedImage(img.Labels, img.RepoDigests, req.Repos):
			out.ImagesOther += own
		case dangling(img) && !slices.Contains(req.Keep, img.ID):
			out.ImagesReclaimable += own
		default:
			out.ImagesKept += own
		}
	}
	// TotalSize counts each layer once; what no single image owns is shared.
	out.ImagesShared = max(res.Images.TotalSize-unique, 0)
	for _, v := range res.Volumes.Items {
		var size int64
		if v.UsageData != nil {
			size = max(v.UsageData.Size, 0)
		}
		if isManaged(v.Labels) {
			out.Volumes = append(out.Volumes, node.VolumeUsage{Name: v.Name, Service: v.Labels[node.LabelService], Bytes: size})
		} else {
			out.OtherVolumes += size
		}
	}
	out.Containers = max(res.Containers.TotalSize, 0)
	if r.DataDir != "" {
		out.DataDir = dirSize(r.DataDir)
	}
	return out, nil
}

// dfAttempts is how often systemDF asks Docker. Docker fails the whole
// call when a container goes away while it is counted (a deploy's
// cutover), which a second try a moment later doesn't hit.
const dfAttempts = 3

// systemDF is Docker's disk usage of containers, images and volumes,
// asked again when a container went away meanwhile.
func (r *Runtime) systemDF(ctx context.Context) (client.DiskUsageResult, error) {
	var err error
	for attempt := range dfAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return client.DiskUsageResult{}, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		var res client.DiskUsageResult
		if res, err = r.cli.DiskUsage(ctx, client.DiskUsageOptions{Containers: true, Images: true, Volumes: true, Verbose: true}); err == nil {
			return res, nil
		}
	}
	return client.DiskUsageResult{}, fmt.Errorf("docker disk usage: %w", err)
}

// dangling reports whether an image has no tag left, as Docker's dangling
// filter (which PruneImages lists with) sees it.
func dangling(img image.Summary) bool {
	return !slices.ContainsFunc(img.RepoTags, func(t string) bool { return t != "<none>:<none>" })
}

// dirSize is the size of the regular files under dir; what it can't read
// isn't counted.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip what can't be read
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
