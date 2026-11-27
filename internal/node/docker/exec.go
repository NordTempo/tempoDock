package docker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// execStopGrace is how long a session's processes get after SIGHUP (a
// terminal's hangup) before they are killed.
const execStopGrace = 2 * time.Second

// stopExecScript stops every process in the container whose environment
// carries the session's marker ($1): SIGHUP, then SIGKILL for those left
// after $2 tenths of a second. It needs sh, tr and grep (busybox or
// coreutils); an image without them keeps the processes, as `docker
// exec` would.
const stopExecScript = `stop() { found=; for p in /proc/[0-9]*; do
  [ "$p" = "/proc/$$" ] && continue
  if tr '\0' '\n' < "$p/environ" 2>/dev/null | grep -qx "` + node.ExecSessionVar + `=$1"; then found=1; kill -"$2" "${p#/proc/}" 2>/dev/null; fi
done; }
stop "$1" HUP; i=0
while [ -n "$found" ] && [ "$i" -lt "$2" ]; do sleep 0.1; i=$((i+1)); stop "$1" 0; done
[ -n "$found" ] && stop "$1" KILL; true`

// Exec implements node.Containers: a Docker exec in the container,
// attached to req.Stream (output, optional input, a terminal and its
// resizes), its exit code from the exec's inspect once its output ended.
// When ctx ends first, the session's processes are stopped (stopExec):
// Docker leaves an exec running when its attach closes.
func (r *Runtime) Exec(ctx context.Context, req node.ExecRequest) (int, error) {
	st := req.Stream
	if st == nil || st.Stdout == nil || len(req.Cmd) == 0 || req.Session == "" {
		return 0, errors.New("exec needs a command, a session and a stream")
	}
	if err := r.execTarget(ctx, req); err != nil {
		return 0, err
	}
	opts := client.ExecCreateOptions{
		Cmd: req.Cmd, Env: []string{node.ExecSessionVar + "=" + req.Session},
		TTY: st.TTY, AttachStdin: st.Stdin != nil, AttachStdout: true, AttachStderr: true,
	}
	if st.TTY && st.Size.Cols > 0 && st.Size.Rows > 0 {
		opts.ConsoleSize = client.ConsoleSize{Height: uint(st.Size.Rows), Width: uint(st.Size.Cols)}
	}
	created, err := r.cli.ExecCreate(ctx, req.ContainerID, opts)
	if err != nil {
		return 0, fmt.Errorf("exec in container %s: %w", req.ContainerID, err)
	}
	att, err := r.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: st.TTY, ConsoleSize: opts.ConsoleSize})
	if err != nil {
		return 0, fmt.Errorf("exec in container %s: %w", req.ContainerID, err)
	}
	defer att.Close()
	ended := make(chan struct{})
	defer close(ended)
	if st.TTY {
		go r.resizeExec(ctx, created.ID, st, ended)
	}
	if st.Stdin != nil {
		go func() {
			_, err := io.Copy(att.Conn, st.Stdin)
			if st.TTY {
				// Closing a terminal session's input makes Docker end its
				// output too; a terminal's end of input is Ctrl-D.
				if err == nil {
					_, _ = att.Conn.Write([]byte{4})
				}
				return
			}
			_ = att.CloseWrite()
		}()
	}
	copied := make(chan error, 1)
	go func() {
		var err error
		if st.TTY {
			_, err = io.Copy(st.Stdout, att.Reader)
		} else {
			_, err = stdcopy.StdCopy(st.Stdout, cmp.Or(st.Stderr, st.Stdout), att.Reader)
		}
		copied <- err
	}()
	select {
	case err := <-copied:
		if err != nil && !errors.Is(err, io.EOF) {
			// The caller stopped taking output: the session ends with it.
			r.stopExec(req)
			return 0, fmt.Errorf("exec output: %w", err)
		}
	case <-ctx.Done():
		att.Close()
		// Stopped first: the copy may be blocked in a write to a caller
		// who stopped reading, which only their end of the stream (a
		// write deadline) frees.
		r.stopExec(req)
		// The attach is closed, so the copy ends, unless it is in such a
		// write: then the stream gets that one write after Exec returned
		// (node.Containers.Exec says so).
		select {
		case err := <-copied:
			if err != nil {
				slog.Debug("exec: output after the session ended", "container", req.ContainerID, "err", err)
			}
		case <-time.After(5 * time.Second):
		}
		return 0, ctx.Err()
	}
	// The output ends as the process does; its exit code may take a
	// moment to show in the inspect.
	for range 50 {
		res, err := r.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			r.stopExec(req)
			return 0, fmt.Errorf("inspect exec: %w", err)
		}
		if !res.Running {
			return res.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			r.stopExec(req)
			return 0, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	// It closed its output but runs on: the session is over all the same.
	r.stopExec(req)
	return 0, errors.New("the command closed its output but kept running, so it was stopped")
}

// execTarget checks that req's container is a running app container
// with the labels core expects there.
func (r *Runtime) execTarget(ctx context.Context, req node.ExecRequest) error {
	res, err := r.cli.ContainerInspect(ctx, req.ContainerID, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return notFound("container", req.ContainerID)
	}
	if err != nil {
		return fmt.Errorf("inspect container %s: %w", req.ContainerID, err)
	}
	c := res.Container
	if c.Config == nil || !isApp(c.Config.Labels) {
		return notFound("container", req.ContainerID)
	}
	if l := node.LabelsFrom(c.Config.Labels); l.Service == "" || !l.Matches(req.Labels) {
		return notFound("container", req.ContainerID)
	}
	if c.State == nil || c.State.Status != container.StateRunning {
		return fmt.Errorf("container %s: %w", req.ContainerID, node.ErrNotRunning)
	}
	return nil
}

// resizeExec gives a terminal session each size from st.Resize until it
// ended (the first came with the create).
func (r *Runtime) resizeExec(ctx context.Context, id string, st *node.JobStream, ended <-chan struct{}) {
	for {
		select {
		case s, ok := <-st.Resize:
			if !ok {
				return
			}
			if s.Cols > 0 && s.Rows > 0 {
				_, _ = r.cli.ExecResize(ctx, id, client.ExecResizeOptions{Height: uint(s.Rows), Width: uint(s.Cols)})
			}
		case <-ended:
			return
		case <-ctx.Done():
			return
		}
	}
}

// stopExec stops what an ended session left running in its container:
// a second exec, as root, that finds the processes by their marker
// (stopExecScript). Best effort, bounded, outliving the session's ctx.
func (r *Runtime) stopExec(req node.ExecRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), execStopGrace+10*time.Second)
	defer cancel()
	tenths := strconv.Itoa(int(execStopGrace / (100 * time.Millisecond)))
	created, err := r.cli.ExecCreate(ctx, req.ContainerID, client.ExecCreateOptions{
		User: "0", AttachStdout: true, AttachStderr: true,
		Cmd: []string{"/bin/sh", "-c", stopExecScript, "dock-exec-stop", req.Session, tenths},
	})
	var att client.ExecAttachResult
	if err == nil {
		att, err = r.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	}
	if err != nil {
		slog.Debug("exec: stop a session's processes", "container", req.ContainerID, "err", err)
		return
	}
	defer att.Close()
	// The client stops watching ctx once the attach is upgraded: a stop
	// that hangs is cut off at ctx's end.
	stop := context.AfterFunc(ctx, att.Close)
	defer stop()
	// Its output ends when it does, so the next session never sees the
	// old one's processes.
	_, _ = io.Copy(io.Discard, att.Reader)
}
