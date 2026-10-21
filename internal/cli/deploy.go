package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/pack"
	"github.com/NordTempo/tempoDock/internal/repoconfig"
)

func newDeployCmd(g *globals) *cobra.Command {
	var f targetFlags
	var image string
	var detach bool
	cmd := &cobra.Command{
		Use:   "deploy [dir]",
		Short: "Deploy a directory's source, or an image",
		Long: `Pack the directory (default: the current one) into a tarball, honoring
.dockerignore (or .gitignore files when there is none), stream it to the
server, and follow the build, start, health check and cutover as they
happen. With --image, deploy a prebuilt image instead.

The new release only receives traffic once its health check passes. If any
step fails, the previous release keeps serving and the exit code is 6.

A service named with --service that doesn't exist yet is created first.`,
		Example: `  dock deploy
  dock deploy ./api -s api
  dock deploy --image ghcr.io/acme/web:1.4 --detach`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if image != "" && len(args) > 0 {
				return usageError("pass a directory or --image, not both", "`dock deploy <dir>` builds source; `dock deploy --image <ref>` deploys an image.")
			}
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			var src *source
			if image == "" {
				var err error
				if src, err = readSource(dir); err != nil {
					return err
				}
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			svc, err := g.ensureService(ctx, s, t, f.service != "")
			if err != nil {
				return err
			}
			return g.startAndFollow(ctx, s, t, detach, func() (apitypes.Release, error) {
				if src == nil {
					g.io.Progressf("Deploying image %s to %s", image, t)
					return send[apitypes.Release](ctx, s, http.MethodPost, t.servicePath()+"/releases", apitypes.DeployImageRequest{Image: image})
				}
				return g.upload(ctx, s, t, src, svc)
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&image, "image", "", "deploy this image instead of building source")
	addDetachFlag(cmd, &detach)
	return cmd
}

// ensureService fetches the target service, creating it when the user named
// it explicitly and it doesn't exist yet.
func (g *globals) ensureService(ctx context.Context, s *session, t target, explicit bool) (apitypes.Service, error) {
	svc, err := get[apitypes.Service](ctx, s, t.servicePath())
	if output.ExitCodeOf(err) != output.ExitNotFound {
		return svc, err
	}
	if !explicit {
		return svc, &output.Error{
			Exit: output.ExitNotFound, Err: err,
			Hint: "The linked service no longer exists. Pass --service to deploy to (and create) another, or relink with `dock link`.",
		}
	}
	svc, err = send[apitypes.Service](ctx, s, http.MethodPost, t.envPath()+"/services", apitypes.CreateServiceRequest{Name: t.Service})
	if err != nil {
		return svc, err
	}
	g.io.Progressf("Created service %s in %s/%s", svc.Name, svc.Project, svc.Environment)
	return svc, nil
}

// source is a directory to deploy, checked before anything is sent.
type source struct {
	dir  string
	repo *repoconfig.Config // dock.toml, if present
	// notes name the files that shape the upload, for the progress line.
	notes []string
}

func readSource(dir string) (*source, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, usageError(fmt.Sprintf("%s is not a directory", dir), "Pass the directory to deploy, or --image to deploy an image.")
	}
	rc, found, err := repoconfig.Load(abs)
	if err != nil {
		return nil, usageError(fmt.Sprintf("%s: %v", filepath.Join(dir, repoconfig.FileName), err), "Fix the file; see docs/dock-toml.md.")
	}
	src := &source{dir: abs}
	if found {
		src.repo = &rc
		src.notes = append(src.notes, "dock.toml")
	}
	if _, err := os.Stat(filepath.Join(abs, ".dockerignore")); err == nil {
		src.notes = append(src.notes, ".dockerignore")
	}
	return src, nil
}

// errUploadEnded stops the packer when the server answered before reading
// the whole upload (e.g. a deploy is already running).
var errUploadEnded = errors.New("upload ended")

// upload packs src and streams it to the server while packing: the
// tarball is never held in memory or written to disk.
func (g *globals) upload(ctx context.Context, s *session, t target, src *source, svc apitypes.Service) (apitypes.Release, error) {
	opts := pack.Options{Dockerfile: svc.DockerfilePath}
	if src.repo != nil && src.repo.Build.Dockerfile != "" {
		opts.Dockerfile = src.repo.Build.Dockerfile
	}
	using := ""
	if len(src.notes) > 0 {
		using = " (using " + strings.Join(src.notes, ", ") + ")"
	}
	g.io.Progressf("Uploading %s to %s%s", filepath.Base(src.dir)+"/", t, using)

	pr, pw := io.Pipe()
	sent := &countingWriter{w: pw}
	type packed struct {
		stats pack.Stats
		err   error
	}
	done := make(chan packed, 1)
	go func() {
		st, err := pack.Write(sent, src.dir, opts)
		_ = pw.CloseWithError(err)
		done <- packed{st, err}
	}()

	req, err := s.api.NewRequest(ctx, http.MethodPost, t.servicePath()+"/releases/source", pr)
	if err != nil {
		_ = pr.CloseWithError(errUploadEnded)
		<-done
		return apitypes.Release{}, err
	}
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := s.api.Do(req)
	_ = pr.CloseWithError(errUploadEnded)
	p := <-done
	if p.err != nil && !errors.Is(p.err, errUploadEnded) {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return apitypes.Release{}, fmt.Errorf("pack %s: %w", src.dir, p.err)
	}
	if err != nil {
		return apitypes.Release{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var rel apitypes.Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return rel, fmt.Errorf("decode the server's answer to the upload: %w", err)
	}
	g.io.Progressf("Uploaded %s, %s (%s compressed)", plural(p.stats.Files, "file"), bytesize(p.stats.Bytes), bytesize(sent.n))
	return rel, nil
}

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
