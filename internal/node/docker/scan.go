package docker

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Image vulnerability scanning (ADR 0074) on a builder. Three kinds of
// job container, each removed when it ends and killed when its ctx does:
//
//   - SBOM: a container of the scanned image itself, its entrypoint
//     replaced by Syft, bind-mounted read-only from the data dir (copied
//     out of the pinned Syft image once). No network, read-only root, no
//     capabilities but DAC_OVERRIDE (to read every file), CPU weight 2
//     (normal is 1024). Running inside the image needs no Docker socket and no copy
//     of the image, and works on both of Docker's image stores and on
//     images without a shell.
//   - Match: the pinned Grype image with the database volume read-only
//     and the SBOM on stdin, no network.
//   - Database: the Grype image with the volume writable, on the
//     builder's network (the one call with network access).
//
// scanCPUShares is the CPU weight of every scan job: next to anything
// else that wants the CPU (a build, an app), a scan gets almost none.
const scanCPUShares = 2

// scanTmp is the scan job's tmpfs, for catalogers that unpack archives.
const scanTmp = "/dock-scan-tmp"

// scanBin is where the Syft binary is mounted in an SBOM job.
const scanBin = "/dock-scan/syft"

// scanDB is where the database volume is mounted in Grype's jobs.
const scanDB = "/db"

// scanState is what a runtime keeps for scans: the extracted Syft binary
// per image, so it is copied once per daemon start.
type scanState struct {
	mu   sync.Mutex
	syft map[string]string
}

// scanDBVolume is the server's vulnerability database volume, one per
// runtime (its owner), so two installs on one Docker host don't share it.
func (r *Runtime) scanDBVolume() string {
	sum := sha256.Sum256([]byte(r.owner()))
	return "dock_scan_db_" + hex.EncodeToString(sum[:6])
}

// SBOM implements node.Scanner.
func (r *Runtime) SBOM(ctx context.Context, req node.SBOMRequest, out io.Writer) (node.SBOMResult, error) {
	var res node.SBOMResult
	if r.DataDir == "" {
		return res, errors.New("scanning needs the runtime's data dir")
	}
	syft, err := r.syftBinary(ctx, req.Images.Syft)
	if err != nil {
		return res, err
	}
	target, pulled, err := r.scanTarget(ctx, req)
	if err != nil {
		return res, err
	}
	if pulled {
		res.Pulled = true
		defer func() {
			// Pulled only for the scan: CI images carry no dock label, so
			// nothing else would ever remove them (ADR 0020 amendment).
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = r.RemoveImage(cctx, req.Pull)
		}()
	}
	cfg := &container.Config{
		Image:      target,
		Labels:     r.scanLabels(req.Labels),
		User:       "0:0",
		Entrypoint: []string{scanBin},
		Cmd: []string{"scan", "dir:/", "--override-default-catalogers", "image", "-o", "syft-json", "-q",
			"--exclude", "./proc/**", "--exclude", "./sys/**", "--exclude", "./dev/**",
			"--exclude", "./dock-scan/**", "--exclude", "." + scanTmp + "/**"},
		// The image's own variables are merged in by Docker; Syft's are
		// set here, and it starts in the empty tmpfs with its home there,
		// so no .syft.yaml in the image can reconfigure its own scan.
		Env: []string{
			"TMPDIR=" + scanTmp, "HOME=" + scanTmp, "XDG_CONFIG_HOME=" + scanTmp,
			"SYFT_CHECK_FOR_APP_UPDATE=false", "SYFT_FILE_METADATA_SELECTION=none", "SYFT_PARALLELISM=1", "SYFT_LOG_LEVEL=error",
		},
		WorkingDir:   scanTmp,
		Healthcheck:  noHealthcheck(),
		AttachStdout: true, AttachStderr: true,
	}
	host := scanHost(req.Limits)
	// Root reads every file through DAC_OVERRIDE, not DAC_READ_SEARCH:
	// with that one Docker's default seccomp profile allows
	// open_by_handle_at, which opens host files through the bind mount
	// below (the "Shocker" escape) for a Syft an image's files exploited.
	// DAC_OVERRIDE also lets it write anywhere, but the root and the
	// mount are read-only.
	host.CapAdd = []string{"DAC_OVERRIDE"}
	host.Tmpfs = map[string]string{scanTmp: "size=256m"}
	host.Mounts = []mount.Mount{{Type: mount.TypeBind, Source: syft, Target: scanBin, ReadOnly: true}}
	var stats sbomStats
	err = r.runScanJob(ctx, "sbom", cfg, host, req.Limits, nil, func(stdout io.Reader) error {
		return stats.read(io.TeeReader(io.LimitReader(stdout, node.MaxSBOMBytes+1), &countingWriter{w: out, n: &res.Bytes}))
	})
	if res.Bytes > node.MaxSBOMBytes {
		return res, fmt.Errorf("the SBOM is over %d MB", node.MaxSBOMBytes>>20)
	}
	if err != nil {
		return res, err
	}
	res.Distro, res.Packages = stats.distro, stats.packages
	return res, nil
}

// scanTarget returns the image to scan: req.Image when it is on the
// server, else req.Pull, pulled now (pulled true).
func (r *Runtime) scanTarget(ctx context.Context, req node.SBOMRequest) (string, bool, error) {
	if req.Image != "" {
		if ok, err := r.HasImage(ctx, req.Image); err != nil || ok {
			return req.Image, false, err
		}
	}
	if req.Pull == "" {
		return "", false, fmt.Errorf("image %s: %w on this server", req.Image, node.ErrNotFound)
	}
	if ok, err := r.HasImage(ctx, req.Pull); err != nil || ok {
		return req.Pull, false, err
	}
	id, err := r.Pull(ctx, req.Pull, nil, req.PullOptions)
	if err != nil {
		return "", false, fmt.Errorf("pull %s: %w", req.Pull, err)
	}
	return id, true, nil
}

// Match implements node.Scanner.
func (r *Runtime) Match(ctx context.Context, req node.MatchRequest, sbom io.Reader, found func(node.Finding) error) (node.MatchResult, error) {
	var res node.MatchResult
	if err := r.ensureScanImage(ctx, req.Images.Grype); err != nil {
		return res, err
	}
	cfg := &container.Config{
		Image:       req.Images.Grype,
		Labels:      r.scanLabels(req.Labels),
		Cmd:         []string{"-o", "json", "-q"},
		Env:         grypeEnv(),
		Healthcheck: noHealthcheck(),
		OpenStdin:   true, StdinOnce: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
	}
	host := scanHost(req.Limits)
	host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.scanDBVolume(), Target: scanDB, ReadOnly: true}}
	host.Tmpfs = map[string]string{"/tmp": "size=64m"}
	err := r.runScanJob(ctx, "match", cfg, host, req.Limits, sbom, func(stdout io.Reader) error {
		return readMatches(stdout, &res, found)
	})
	if err != nil && strings.Contains(err.Error(), "database does not exist") {
		return res, node.ErrNoScanDatabase
	}
	return res, err
}

// scanDatabaseMemory caps the jobs that write the vulnerability database
// (node.ScanDatabaseMemory). Matches only read it (1 GB).
const scanDatabaseMemory = node.ScanDatabaseMemory

// ScanDatabase implements node.Scanner.
func (r *Runtime) ScanDatabase(ctx context.Context, req node.ScanDatabaseRequest) (node.ScanDatabase, error) {
	if err := r.ensureScanImage(ctx, req.Images.Grype); err != nil {
		return node.ScanDatabase{}, err
	}
	if err := r.EnsureVolume(ctx, r.scanDBVolume(), req.Labels); err != nil {
		return node.ScanDatabase{}, err
	}
	limits := node.ScanLimits{MemoryBytes: scanDatabaseMemory, Timeout: 15 * time.Minute}
	if req.Update {
		if r.ScanNetwork == "" {
			return node.ScanDatabase{}, errors.New("this server doesn't build, so it has no network for the vulnerability database")
		}
		if err := r.cleanScanLeftovers(ctx, req); err != nil {
			return node.ScanDatabase{}, err
		}
		cfg := &container.Config{
			Image: req.Images.Grype, Labels: r.scanLabels(req.Labels), Cmd: []string{"db", "update"},
			Env: grypeEnv(), Healthcheck: noHealthcheck(), AttachStdout: true, AttachStderr: true,
		}
		host := scanHost(limits)
		host.NetworkMode = container.NetworkMode(r.ScanNetwork)
		host.ReadonlyRootfs = false
		host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.scanDBVolume(), Target: scanDB}}
		if err := r.runScanJob(ctx, "db-update", cfg, host, limits, nil, discard); err != nil {
			return node.ScanDatabase{}, err
		}
	}
	cfg := &container.Config{
		Image: req.Images.Grype, Labels: r.scanLabels(req.Labels), Cmd: []string{"db", "status", "-o", "json"},
		Env: grypeEnv(), Healthcheck: noHealthcheck(), AttachStdout: true, AttachStderr: true,
	}
	host := scanHost(limits)
	host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.scanDBVolume(), Target: scanDB, ReadOnly: true}}
	var st struct {
		Schema string    `json:"schemaVersion"`
		Built  time.Time `json:"built"`
		Valid  bool      `json:"valid"`
	}
	err := r.runScanJob(ctx, "db-status", cfg, host, limits, nil, func(stdout io.Reader) error {
		return json.NewDecoder(io.LimitReader(stdout, 1<<20)).Decode(&st)
	})
	if err != nil {
		if !req.Update {
			// No database yet: grype's status fails, and that is the answer.
			return node.ScanDatabase{}, nil
		}
		return node.ScanDatabase{}, err
	}
	if !st.Valid {
		return node.ScanDatabase{Schema: st.Schema}, nil
	}
	return node.ScanDatabase{Built: st.Built, Schema: st.Schema}, nil
}

// RemoteDigest implements node.Scanner: one manifest request to ref's
// registry with the given credentials, nothing pulled.
func (r *Runtime) RemoteDigest(ctx context.Context, ref string, opts node.PullOptions) (string, error) {
	var auth string
	switch {
	case opts.Token != "":
		auth = RegistryAuth(opts.Token)
	case opts.Username != "":
		auth = PasswordAuth(ref, opts.Username, opts.Password)
	}
	res, err := r.cli.DistributionInspect(ctx, ref, client.DistributionInspectOptions{EncodedRegistryAuth: auth})
	if err != nil {
		return "", fmt.Errorf("ask the registry for %s: %w", ref, err)
	}
	return res.Descriptor.Digest.String(), nil
}

// ImportScanDatabase implements node.Scanner: the archive is written to
// a file in the server's data dir, mounted read-only into a Grype job
// (no network) that imports it into the database volume, and removed.
func (r *Runtime) ImportScanDatabase(ctx context.Context, req node.ScanDatabaseRequest, archive io.Reader, size int64) (node.ScanDatabase, error) {
	if size <= 0 || size > node.MaxScanDatabaseArchive {
		return node.ScanDatabase{}, fmt.Errorf("a database archive is 1 byte to %d MB, not %d bytes", node.MaxScanDatabaseArchive>>20, size)
	}
	if err := r.ensureScanImage(ctx, req.Images.Grype); err != nil {
		return node.ScanDatabase{}, err
	}
	if err := r.EnsureVolume(ctx, r.scanDBVolume(), req.Labels); err != nil {
		return node.ScanDatabase{}, err
	}
	if err := r.cleanScanLeftovers(ctx, req); err != nil {
		return node.ScanDatabase{}, err
	}
	dir := filepath.Join(r.DataDir, "scan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return node.ScanDatabase{}, err
	}
	f, err := os.CreateTemp(dir, "import-*.tar.zst")
	if err != nil {
		return node.ScanDatabase{}, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	n, err := io.Copy(f, io.LimitReader(archive, size))
	if err = errors.Join(err, f.Chmod(0o644), f.Close()); err != nil {
		return node.ScanDatabase{}, err
	}
	if n != size {
		return node.ScanDatabase{}, fmt.Errorf("the database archive ended after %d of %d bytes", n, size)
	}
	const in = "/dock-import/db.tar.zst"
	limits := node.ScanLimits{MemoryBytes: scanDatabaseMemory, Timeout: 15 * time.Minute}
	cfg := &container.Config{
		Image: req.Images.Grype, Labels: r.scanLabels(req.Labels), Cmd: []string{"db", "import", in},
		Env: grypeEnv(), Healthcheck: noHealthcheck(), AttachStdout: true, AttachStderr: true,
	}
	host := scanHost(limits)
	host.ReadonlyRootfs = false
	host.Mounts = []mount.Mount{
		{Type: mount.TypeVolume, Source: r.scanDBVolume(), Target: scanDB},
		{Type: mount.TypeBind, Source: f.Name(), Target: in, ReadOnly: true},
	}
	if err := r.runScanJob(ctx, "db-import", cfg, host, limits, nil, discard); err != nil {
		return node.ScanDatabase{}, err
	}
	return r.ScanDatabase(ctx, node.ScanDatabaseRequest{Images: req.Images, Labels: req.Labels})
}

// ImageDigest implements node.Scanner: the repo digest of ref's
// repository, as Docker recorded it when pulling.
func (r *Runtime) ImageDigest(ctx context.Context, ref string) (string, error) {
	img, err := r.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return "", fmt.Errorf("image %s: %w", ref, node.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", ref, err)
	}
	return pickRepoDigest(ref, img.RepoDigests), nil
}

// pickRepoDigest returns the digest of ref's repository among digests
// (repo@sha256:...): Docker records one per repository an image was
// pulled from, and they may differ (re-pushed elsewhere). The repository
// is compared as written and as Docker names it (docker.io/library/...).
func pickRepoDigest(ref string, digests []string) string {
	repo := ref
	if i := strings.IndexByte(repo, '@'); i >= 0 {
		repo = repo[:i]
	} else if i := strings.LastIndexByte(repo, ':'); i > strings.LastIndexByte(repo, '/') {
		repo = repo[:i]
	}
	for _, d := range digests {
		r, _, ok := strings.Cut(d, "@")
		if ok && (r == repo || r == "docker.io/"+repo || r == "docker.io/library/"+repo) {
			return d
		}
	}
	return ""
}

// grypeEnv configures Grype's jobs: the database in the volume, never
// updated or refused for its age by a match (its age is shown instead).
func grypeEnv() []string {
	return []string{
		"GRYPE_DB_CACHE_DIR=" + scanDB, "GRYPE_DB_AUTO_UPDATE=false", "GRYPE_DB_VALIDATE_AGE=false",
		"GRYPE_CHECK_FOR_APP_UPDATE=false", "GRYPE_LOG_LEVEL=error",
	}
}

// scanLabels are a scan job's labels: the control plane's, as a job of
// this runtime (node.LabelOwner), so UnpauseStranded removes one a
// restart cut short instead of leaving it for good.
func (r *Runtime) scanLabels(l node.Labels) map[string]string {
	m := l.Map()
	m[node.LabelRole] = node.RoleJob
	if owner := r.owner(); owner != "" {
		m[node.LabelOwner] = owner
	}
	return m
}

// scanHost is the host config every scan job shares.
func scanHost(l node.ScanLimits) *container.HostConfig {
	mem := l.MemoryBytes
	if mem <= 0 {
		mem = 1 << 30
	}
	pids := int64(256)
	return &container.HostConfig{
		NetworkMode:    "none",
		RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyDisabled},
		LogConfig:      container.LogConfig{Type: "local", Config: map[string]string{"max-size": "1m", "max-file": "1", "compress": "false"}},
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Resources: container.Resources{
			CPUShares: scanCPUShares, NanoCPUs: 1e9, Memory: mem, MemorySwap: NoSwap(mem),
			PidsLimit: &pids, BlkioWeight: 10,
		},
	}
}

// discard reads a job's output to its end.
func discard(r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// runScanJob creates, attaches to, starts and waits for one scan job,
// handing its stdout to read while it runs (stdin, when set, is copied to
// the job's standard input). Stderr's tail explains a failure. The
// container is force-removed however this returns, ctx ending included,
// which is how a scan is preempted.
func (r *Runtime) runScanJob(ctx context.Context, kind string, cfg *container.Config, host *container.HostConfig, l node.ScanLimits, stdin io.Reader, read func(io.Reader) error) error {
	if l.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.Timeout)
		defer cancel()
	}
	created, err := r.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: host})
	if err != nil {
		return fmt.Errorf("create scan job (%s): %w", kind, err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = r.cli.ContainerRemove(cctx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	}()
	att, err := r.cli.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{Stream: true, Stdin: stdin != nil, Stdout: true, Stderr: true})
	if err != nil {
		return fmt.Errorf("attach to scan job (%s): %w", kind, err)
	}
	defer att.Close()
	wait := r.cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := r.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start scan job (%s): %w", kind, err)
	}
	if stdin != nil {
		go func() {
			_, _ = io.Copy(att.Conn, stdin)
			_ = att.CloseWrite()
		}()
	}
	pr, pw := io.Pipe()
	var stderr tailBuffer
	go func() {
		_, err := stdcopy.StdCopy(pw, &stderr, att.Reader)
		pw.CloseWithError(err)
	}()
	readErr := make(chan error, 1)
	go func() {
		err := read(pr)
		// Whatever read left is drained, so the job never blocks on a
		// full pipe.
		_, _ = io.Copy(io.Discard, pr)
		readErr <- err
	}()
	var code int64
	select {
	case w := <-wait.Result:
		code = w.StatusCode
		if w.Error != nil && w.Error.Message != "" {
			return fmt.Errorf("scan job (%s): %s", kind, w.Error.Message)
		}
	case err := <-wait.Error:
		return fmt.Errorf("wait for scan job (%s): %w", kind, err)
	case <-ctx.Done():
		return ctx.Err()
	}
	var rerr error
	select {
	case rerr = <-readErr:
	case <-time.After(10 * time.Second):
		rerr = errors.New("its output didn't end")
	case <-ctx.Done():
		return ctx.Err()
	}
	if code == 137 && r.oomKilled(ctx, created.ID) {
		return fmt.Errorf("scan job (%s) ran out of memory (%d MB); raise [scan] memory_mb", kind, host.Memory>>20)
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return fmt.Errorf("scan job (%s) exited with %d: %s", kind, code, msg)
	}
	if rerr != nil {
		return fmt.Errorf("scan job (%s) output: %w", kind, rerr)
	}
	return nil
}

// oomKilled reports whether a container was killed for its memory.
func (r *Runtime) oomKilled(ctx context.Context, id string) bool {
	c, err := r.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	return err == nil && c.Container.State != nil && c.Container.State.OOMKilled
}

// ensureScanImage pulls a scanner image (anonymously, from its public
// registry) when it isn't on the server.
func (r *Runtime) ensureScanImage(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("no scanner image configured ([scan])")
	}
	if ok, err := r.HasImage(ctx, ref); err != nil || ok {
		return err
	}
	if _, err := r.Pull(ctx, ref, nil); err != nil {
		return fmt.Errorf("pull scanner image %s: %w", ref, err)
	}
	return nil
}

// syftBinary returns the host path of the Syft binary of image, copied
// out of it into the data dir the first time (a container is created,
// never started, and its /syft read through Docker's archive API).
func (r *Runtime) syftBinary(ctx context.Context, image string) (string, error) {
	r.scan.mu.Lock()
	defer r.scan.mu.Unlock()
	if p, ok := r.scan.syft[image]; ok {
		return p, nil
	}
	sum := sha256.Sum256([]byte(image))
	dir := filepath.Join(r.DataDir, "scan")
	path := filepath.Join(dir, "syft-"+hex.EncodeToString(sum[:6]))
	if _, err := os.Stat(path); err != nil {
		if err := r.ensureScanImage(ctx, image); err != nil {
			return "", err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := r.copyOut(ctx, image, "/syft", path); err != nil {
			return "", fmt.Errorf("copy syft out of %s: %w", image, err)
		}
	}
	if r.scan.syft == nil {
		r.scan.syft = map[string]string{}
	}
	r.scan.syft[image] = path
	return path, nil
}

// copyOut copies the regular file src out of image to dst (mode 0755),
// through a temporary file so dst is never half written.
func (r *Runtime) copyOut(ctx context.Context, image, src, dst string) error {
	created, err := r.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: image, Labels: r.scanLabels(node.Labels{})},
		HostConfig: &container.HostConfig{NetworkMode: "none"},
	})
	if err != nil {
		return err
	}
	defer func() {
		_, _ = r.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	}()
	rc, err := r.cli.CopyFromContainer(ctx, created.ID, client.CopyFromContainerOptions{SourcePath: src})
	if err != nil {
		return err
	}
	defer func() { _ = rc.Content.Close() }()
	tr := tar.NewReader(rc.Content)
	for {
		h, err := tr.Next()
		if err != nil {
			return fmt.Errorf("%s not found in the image: %w", src, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(dst), ".syft-*")
		if err != nil {
			return err
		}
		_, err = io.Copy(tmp, io.LimitReader(tr, 512<<20))
		err = errors.Join(err, tmp.Chmod(0o755), tmp.Close())
		if err == nil {
			err = os.Rename(tmp.Name(), dst)
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
		return err
	}
}

// countingWriter counts what it writes through.
type countingWriter struct {
	w io.Writer
	n *int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	*c.n += int64(n)
	return n, err
}

// sbomStats reads what the control plane shows of an SBOM as it streams
// past: the distro and the number of packages. It decodes one artifact at
// a time, so memory stays per package.
type sbomStats struct {
	distro   string
	packages int
}

// read walks a syft-json document's top level.
func (s *sbomStats) read(r io.Reader) error {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		switch key {
		case "artifacts":
			if err := expectDelim(dec, '['); err != nil {
				return err
			}
			for dec.More() {
				var skip struct{}
				if err := dec.Decode(&skip); err != nil {
					return err
				}
				s.packages++
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
		case "distro":
			var d struct {
				PrettyName string `json:"prettyName"`
				ID         string `json:"id"`
				VersionID  string `json:"versionID"`
			}
			if err := dec.Decode(&d); err != nil {
				return err
			}
			// "alpine 3.20.3", the words a base image is named by.
			s.distro = strings.TrimSpace(d.ID + " " + d.VersionID)
			if d.ID == "" {
				s.distro = d.PrettyName
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return err
			}
		}
	}
	return nil
}

// expectDelim reads one delimiter token.
func expectDelim(dec *json.Decoder, want json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != want {
		return fmt.Errorf("unexpected %v, want %v", t, want)
	}
	return nil
}

// grypeMatch is the part of one of Grype's matches Dock keeps.
type grypeMatch struct {
	Vulnerability struct {
		ID       string `json:"id"`
		Severity string `json:"severity"`
		Fix      struct {
			Versions []string `json:"versions"`
			State    string   `json:"state"`
		} `json:"fix"`
		KnownExploited []json.RawMessage `json:"knownExploited"`
		EPSS           []struct {
			EPSS float64 `json:"epss"`
		} `json:"epss"`
	} `json:"vulnerability"`
	Artifact struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Type    string `json:"type"`
	} `json:"artifact"`
}

// readMatches walks Grype's JSON report as it streams: each match is
// decoded alone and handed to found (up to node.MaxFindings, the rest
// counted in res.Truncated), and the descriptor gives the database's
// build time.
func readMatches(r io.Reader, res *node.MatchResult, found func(node.Finding) error) error {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	n := 0
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		switch key {
		case "matches":
			if err := expectDelim(dec, '['); err != nil {
				return err
			}
			for dec.More() {
				var m grypeMatch
				if err := dec.Decode(&m); err != nil {
					return err
				}
				if n >= node.MaxFindings {
					res.Truncated++
					continue
				}
				n++
				if err := found(m.finding()); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
		case "descriptor":
			var d struct {
				DB struct {
					Status struct {
						Built time.Time `json:"built"`
					} `json:"status"`
				} `json:"db"`
			}
			if err := dec.Decode(&d); err != nil {
				return err
			}
			res.DatabaseBuilt = d.DB.Status.Built
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return err
			}
		}
	}
	return nil
}

// finding converts a match.
func (m grypeMatch) finding() node.Finding {
	v := m.Vulnerability
	f := node.Finding{
		ID: v.ID, Severity: parseSeverity(v.Severity), Package: m.Artifact.Name, Version: m.Artifact.Version,
		Type: m.Artifact.Type, KEV: len(v.KnownExploited) > 0, EPSS: -1,
	}
	if v.Fix.State == "fixed" && len(v.Fix.Versions) > 0 {
		f.FixedIn = v.Fix.Versions[0]
	}
	if len(v.EPSS) > 0 {
		f.EPSS = v.EPSS[0].EPSS
	}
	return f
}

// parseSeverity reads Grype's severity names.
func parseSeverity(s string) node.Severity {
	switch strings.ToLower(s) {
	case "critical":
		return node.SeverityCritical
	case "high":
		return node.SeverityHigh
	case "medium":
		return node.SeverityMedium
	case "low":
		return node.SeverityLow
	case "negligible":
		return node.SeverityNegligible
	}
	return node.SeverityUnknown
}
