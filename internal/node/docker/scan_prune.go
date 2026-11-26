package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// The vulnerability database's leftovers (ADR 0035 amendment 2). Grype
// downloads an update into a grype-db-download* directory beside the
// database and moves it in at the end; a download that is cut off (a
// deploy preempting the scanner, the job's memory limit or timeout, a
// restart) leaves that directory, up to the database's 3.4 GB, and Grype
// never removes it. Grype's image has no shell, so a job in a small one
// that has (node.ScanImages.Tool) sizes and removes them.

// scanLeftoverGlob matches what cut-off downloads leave, as the tool
// job's shell expands it.
const scanLeftoverGlob = scanDB + "/grype-db-download*"

// scanLeftovers sizes what cut-off downloads left in the database's
// volume and, with remove, removes it; it returns the bytes. A volume
// without leftovers answers 0.
func (r *Runtime) scanLeftovers(ctx context.Context, tool string, labels node.Labels, remove bool) (int64, error) {
	if err := r.ensureScanImage(ctx, tool); err != nil {
		return 0, err
	}
	script := "du -sk " + scanLeftoverGlob + " 2>/dev/null | awk '{s+=$1} END {print s+0}'"
	if remove {
		script += "; rm -rf " + scanLeftoverGlob
	}
	cfg := &container.Config{
		Image: tool, Labels: r.scanLabels(labels), User: "0:0",
		Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{script},
		Healthcheck: noHealthcheck(), AttachStdout: true, AttachStderr: true,
	}
	limits := node.ScanLimits{MemoryBytes: 128 << 20, Timeout: 5 * time.Minute}
	host := scanHost(limits)
	host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.scanDBVolume(), Target: scanDB, ReadOnly: !remove}}
	var kb int64
	err := r.runScanJob(ctx, "db-leftovers", cfg, host, limits, nil, func(stdout io.Reader) error {
		sc := bufio.NewScanner(io.LimitReader(stdout, 4096))
		for sc.Scan() {
			if n, err := strconv.ParseInt(strings.TrimSpace(sc.Text()), 10, 64); err == nil {
				kb = n
			}
		}
		return sc.Err()
	})
	return kb << 10, err
}

// cleanScanLeftovers removes what cut-off downloads left before Grype
// writes the database again, when the request names a tool image.
func (r *Runtime) cleanScanLeftovers(ctx context.Context, req node.ScanDatabaseRequest) error {
	if req.Images.Tool == "" {
		return nil
	}
	if _, err := r.scanLeftovers(ctx, req.Images.Tool, req.Labels, true); err != nil {
		return fmt.Errorf("remove what earlier downloads left: %w", err)
	}
	return nil
}

// PruneScanDatabase implements node.Scanner.
func (r *Runtime) PruneScanDatabase(ctx context.Context, req node.ScanDatabasePrune) (node.PruneResult, error) {
	vol := r.scanDBVolume()
	res, err := r.cli.VolumeInspect(ctx, vol, client.VolumeInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return node.PruneResult{}, nil // no database: nothing to free
	case err != nil:
		return node.PruneResult{}, fmt.Errorf("inspect volume %s: %w", vol, err)
	}
	if !req.All {
		if req.Tool == "" {
			return node.PruneResult{}, errors.New("no image to remove the leftovers with")
		}
		n, err := r.scanLeftovers(ctx, req.Tool, req.Labels, true)
		if err != nil || n == 0 {
			return node.PruneResult{}, err
		}
		return node.PruneResult{Removed: 1, ReclaimedBytes: n}, nil
	}
	size := r.volumeSize(ctx, vol)
	if err := r.RemoveVolume(ctx, vol, node.LabelsFrom(res.Volume.Labels)); err != nil {
		if errors.Is(err, node.ErrNotFound) {
			return node.PruneResult{}, nil
		}
		return node.PruneResult{}, err
	}
	return node.PruneResult{Removed: 1, ReclaimedBytes: size}, nil
}

// volumeSize is a volume's size as Docker's disk usage measures it; 0
// when it can't say.
func (r *Runtime) volumeSize(ctx context.Context, name string) int64 {
	res, err := r.cli.DiskUsage(ctx, client.DiskUsageOptions{Volumes: true, Verbose: true})
	if err != nil {
		return 0
	}
	for _, v := range res.Volumes.Items {
		if v.Name == name && v.UsageData != nil {
			return max(v.UsageData.Size, 0)
		}
	}
	return 0
}
