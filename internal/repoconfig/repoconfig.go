// Package repoconfig parses dock.toml, the optional per-repo config file
// (ADR 0007). It holds what belongs to the code: how to build it and how to
// run it. Anything that differs between environments lives in the platform DB.
//
// The CLI reads it from a directory with Load; the server reads it from an
// uploaded build context with FromTar. Keep this package free of
// dependencies beyond go-toml.
package repoconfig

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the config file's name at the root of a repo.
const FileName = "dock.toml"

// maxSize caps how much of a dock.toml is read, so a hostile upload
// can't make the server buffer an arbitrarily large file.
const maxSize = 64 << 10

// Config is the parsed file. A zero field means "not set by the file", so the
// value stored in the platform DB applies. Parse rejects explicit zero values
// (empty strings, port 0), which keeps that unambiguous.
type Config struct {
	Build  Build
	Deploy Deploy
}

// Build configures how source becomes an image.
type Build struct {
	// Dockerfile is a path relative to the repo root. When unset, a root
	// Dockerfile is used if present, otherwise a Railpack zero-config build.
	Dockerfile string
	// Command is the Railpack build command. Not allowed with Dockerfile.
	Command string
}

// Deploy configures how a release is started and health checked.
type Deploy struct {
	// StartCommand overrides the image's command (run via `sh -c`).
	StartCommand string
	// Port is the container port that receives HTTP traffic.
	Port int
	// HealthCheckPath is an HTTP path that must return 2xx/3xx before the
	// release gets traffic.
	HealthCheckPath string
	// HealthCheckTimeout is how long a new release may take to pass.
	HealthCheckTimeout time.Duration

	// The ongoing health check while the release runs (ADR 0043).
	// HealthCheckInterval is the time between probes, IntervalOff for
	// "off"; HealthCheckProbeTimeout bounds one probe; HealthCheckRetries
	// failed probes in a row make the service unhealthy; failures within
	// HealthCheckStartPeriod after a start don't count.
	HealthCheckInterval     time.Duration
	HealthCheckProbeTimeout time.Duration
	HealthCheckRetries      int
	HealthCheckStartPeriod  time.Duration
	// PreDeployCommand runs (via `sh -c`) as a one-off job with the new
	// image before the release starts, e.g. database migrations (ADR 0045).
	PreDeployCommand string
	// PreDeployTimeout is how long PreDeployCommand may run.
	PreDeployTimeout time.Duration
}

// IntervalOff is Deploy.HealthCheckInterval for
// `health_check_interval = "off"`: no ongoing health check.
const IntervalOff time.Duration = -1

// file mirrors the TOML layout. Durations stay strings until validation so
// errors can name the key.
type file struct {
	Build struct {
		Dockerfile *string `toml:"dockerfile"`
		Command    *string `toml:"command"`
	} `toml:"build"`
	Deploy struct {
		StartCommand       *string `toml:"start_command"`
		Port               *int    `toml:"port"`
		HealthCheckPath    *string `toml:"health_check_path"`
		HealthCheckTimeout *string `toml:"health_check_timeout"`

		HealthCheckInterval     *string `toml:"health_check_interval"`
		HealthCheckProbeTimeout *string `toml:"health_check_probe_timeout"`
		HealthCheckRetries      *int    `toml:"health_check_retries"`
		HealthCheckStartPeriod  *string `toml:"health_check_start_period"`

		PreDeployCommand *string `toml:"pre_deploy_command"`
		PreDeployTimeout *string `toml:"pre_deploy_timeout"`
	} `toml:"deploy"`
}

// Bounds of the deploy section's health check keys; the platform's
// service settings have the same ones.
const (
	maxHealthCheckTimeout = time.Hour
	maxInterval           = time.Hour
	maxProbeTimeout       = 5 * time.Minute
	maxRetries            = 100
	maxStartPeriod        = time.Hour
	maxPreDeployTimeout   = time.Hour
)

// Parse decodes and validates a dock.toml. Unknown keys, wrong types and
// syntax errors are reported with line numbers; all validation problems are
// returned together.
func Parse(b []byte) (Config, error) {
	var f file
	dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Config{}, decodeError(err)
	}

	var cfg Config
	var errs []error
	bad := func(key, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s: %s", FileName, key, fmt.Sprintf(format, a...)))
	}
	nonEmpty := func(key string, v *string) string {
		if v == nil {
			return ""
		}
		if strings.TrimSpace(*v) == "" {
			bad(key, "must not be empty; remove the key to use the default")
		}
		return *v
	}

	cfg.Build.Dockerfile = nonEmpty("build.dockerfile", f.Build.Dockerfile)
	if d := cfg.Build.Dockerfile; d != "" && !filepath.IsLocal(d) {
		bad("build.dockerfile", "%q must be a relative path inside the repo", d)
	}
	cfg.Build.Command = nonEmpty("build.command", f.Build.Command)
	if cfg.Build.Dockerfile != "" && cfg.Build.Command != "" {
		bad("build.command", "only applies to builds without a Dockerfile; put the build step in %s instead", cfg.Build.Dockerfile)
	}

	cfg.Deploy.StartCommand = nonEmpty("deploy.start_command", f.Deploy.StartCommand)
	if p := f.Deploy.Port; p != nil {
		if *p < 1 || *p > 65535 {
			bad("deploy.port", "must be between 1 and 65535, got %d", *p)
		}
		cfg.Deploy.Port = *p
	}
	cfg.Deploy.HealthCheckPath = nonEmpty("deploy.health_check_path", f.Deploy.HealthCheckPath)
	if p := cfg.Deploy.HealthCheckPath; p != "" && !strings.HasPrefix(p, "/") {
		bad("deploy.health_check_path", "%q must start with /", p)
	}
	// duration parses an optional duration key, 1s to most.
	duration := func(key string, s *string, most time.Duration) time.Duration {
		if s == nil {
			return 0
		}
		d, err := time.ParseDuration(*s)
		switch {
		case err != nil:
			bad(key, "%q is not a duration; use a unit, e.g. \"30s\" or \"2m\"", *s)
		case d < time.Second || d > most:
			bad(key, "must be between 1s and %s, got %s", most, d)
		}
		return d
	}
	cfg.Deploy.HealthCheckTimeout = duration("deploy.health_check_timeout", f.Deploy.HealthCheckTimeout, maxHealthCheckTimeout)
	switch s := f.Deploy.HealthCheckInterval; {
	case s != nil && *s == "off":
		cfg.Deploy.HealthCheckInterval = IntervalOff
	case s != nil:
		if _, err := time.ParseDuration(*s); err != nil {
			bad("deploy.health_check_interval", "%q is not a duration; use a unit, e.g. \"30s\", or \"off\" to turn the ongoing check off", *s)
			break
		}
		cfg.Deploy.HealthCheckInterval = duration("deploy.health_check_interval", s, maxInterval)
	}
	cfg.Deploy.HealthCheckProbeTimeout = duration("deploy.health_check_probe_timeout", f.Deploy.HealthCheckProbeTimeout, maxProbeTimeout)
	if n := f.Deploy.HealthCheckRetries; n != nil {
		if *n < 1 || *n > maxRetries {
			bad("deploy.health_check_retries", "must be between 1 and %d, got %d", maxRetries, *n)
		}
		cfg.Deploy.HealthCheckRetries = *n
	}
	cfg.Deploy.HealthCheckStartPeriod = duration("deploy.health_check_start_period", f.Deploy.HealthCheckStartPeriod, maxStartPeriod)
	cfg.Deploy.PreDeployCommand = nonEmpty("deploy.pre_deploy_command", f.Deploy.PreDeployCommand)
	cfg.Deploy.PreDeployTimeout = duration("deploy.pre_deploy_timeout", f.Deploy.PreDeployTimeout, maxPreDeployTimeout)

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// decodeError rewrites go-toml errors as "dock.toml:line:col: message".
func decodeError(err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		errs := make([]error, 0, len(strict.Errors))
		for _, e := range strict.Errors {
			row, col := e.Position()
			errs = append(errs, fmt.Errorf("%s:%d:%d: unknown key %q", FileName, row, col, strings.Join(e.Key(), ".")))
		}
		return errors.Join(errs...)
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return fmt.Errorf("%s:%d:%d: %s", FileName, row, col, describe(de))
	}
	return fmt.Errorf("%s: %w", FileName, err)
}

// describe turns go-toml's type errors, which name Go struct types, into
// "deploy.port must be an integer, not a string".
func describe(de *toml.DecodeError) string {
	msg := strings.TrimPrefix(de.Error(), "toml: ")
	got, ok := strings.CutPrefix(msg, "cannot decode TOML ")
	if !ok || len(de.Key()) == 0 {
		return msg
	}
	got, _, _ = strings.Cut(got, " ")
	want := "a table"
	switch {
	case strings.HasSuffix(msg, " of type int"):
		want = "an integer"
	case strings.HasSuffix(msg, " of type string"):
		want = "a string"
	}
	article := "a"
	if strings.ContainsRune("aeiou", rune(got[0])) {
		article = "an"
	}
	return fmt.Sprintf("%s must be %s, not %s %s", strings.Join(de.Key(), "."), want, article, got)
}

// Load reads dock.toml from dir. found is false when the file doesn't
// exist, which is not an error.
func Load(dir string) (cfg Config, found bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	cfg, err = Parse(b)
	return cfg, true, err
}

// ErrNotArchive is returned (wrapped) by FromTar for input that isn't a
// readable tar or tar.gz stream.
var ErrNotArchive = errors.New("not a tar or tar.gz archive")

// FromTar scans a build context tar stream (plain or gzip) for dock.toml
// at its root and parses it. found is false when the archive has none. It
// consumes r up to the file, or entirely when there is none.
func FromTar(r io.Reader) (cfg Config, found bool, err error) {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return Config{}, false, fmt.Errorf("%w: %w", ErrNotArchive, err)
		}
		defer func() { _ = zr.Close() }()
		src = zr
	}
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return Config{}, false, nil
		}
		if err != nil {
			return Config{}, false, fmt.Errorf("%w: %w", ErrNotArchive, err)
		}
		if h.Typeflag == tar.TypeReg && path.Clean(h.Name) == FileName {
			cfg, err := parseReader(tr)
			return cfg, true, err
		}
	}
}

func parseReader(r io.Reader) (Config, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", FileName, err)
	}
	if len(b) > maxSize {
		return Config{}, fmt.Errorf("%s is larger than %d KiB", FileName, maxSize>>10)
	}
	return Parse(b)
}
