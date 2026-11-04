package docker

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/logarchive"
)

// errNoDataDir is a runtime without a data dir asked to keep logs.
var errNoDataDir = errors.New("this server's runtime has no data dir to keep logs in")

// ArchiveLogs implements node.Containers: it streams the container's
// whole log (timestamps on) into its release's archive (ADR 0046). See
// package logarchive for the format and limits.
func (r *Runtime) ArchiveLogs(ctx context.Context, id string, limits node.LogLimits) (node.LogArchive, error) {
	if r.DataDir == "" {
		return node.LogArchive{}, errNoDataDir
	}
	c, err := r.inspect(ctx, id)
	if err != nil {
		return node.LogArchive{}, err
	}
	labels := node.LabelsFrom(c.Config.Labels)
	out := node.LogArchive{Service: labels.Service, Release: labels.Release}
	if out.Service == "" || out.Release == "" {
		return out, fmt.Errorf("container %s has no service or release label; its logs aren't kept", id)
	}
	rc, err := r.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true})
	if err != nil {
		return out, fmt.Errorf("logs for container %s: %w", id, err)
	}
	defer func() { _ = rc.Close() }()
	out.Bytes, err = logarchive.Write(logarchive.Dir(r.DataDir), out.Service, out.Release, limits, func(add func(node.LogLine) error) error {
		stdout := &lineWriter{stream: "stdout", emit: add}
		stderr := &lineWriter{stream: "stderr", emit: add}
		if _, err := stdcopy.StdCopy(stdout, stderr, rc); err != nil {
			return fmt.Errorf("read the logs of container %s: %w", id, err)
		}
		return errors.Join(stdout.flush(), stderr.flush())
	})
	return out, err
}

// ArchivedLogs implements node.Containers.
func (r *Runtime) ArchivedLogs(ctx context.Context, q node.LogArchiveQuery) (<-chan node.LogLine, error) {
	if r.DataDir == "" {
		return nil, fmt.Errorf("logs of release %s: %w", q.Release, node.ErrNotFound)
	}
	return logarchive.Read(ctx, logarchive.Dir(r.DataDir), q)
}

// PruneLogs implements node.Containers.
func (r *Runtime) PruneLogs(_ context.Context, p node.LogPrune) (node.PruneResult, error) {
	if r.DataDir == "" {
		return node.PruneResult{}, nil
	}
	return logarchive.Prune(logarchive.Dir(r.DataDir), p)
}
