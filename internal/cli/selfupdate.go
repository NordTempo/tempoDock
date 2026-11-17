package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/version"
)

// selfUpdateView is the JSON shape of `dock self-update`.
type selfUpdateView struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Target is how To was picked: "server" (the version of the current
	// context's server), "latest" or "pinned" (--to).
	Target string `json:"target"`
	// Server is the server whose version To is, with Target "server".
	Server string `json:"server,omitempty"`
	// ServerError is why the server's version is unknown, when Target
	// fell back to "latest".
	ServerError string `json:"serverError,omitempty"`
	Asset       string `json:"asset"`
	// Path is the binary that is (or would be) replaced.
	Path string `json:"path"`
	// Updated: Path now holds To. False when it already did, or with
	// --dry-run.
	Updated bool `json:"updated"`
	DryRun  bool `json:"dryRun,omitempty"`
	// Unsigned: To predates signed releases (update.FirstSignedRelease)
	// and was checked against its SHA256SUMS only.
	Unsigned bool `json:"unsigned,omitempty"`
}

// selfUpdateHost is what `dock self-update` takes from its surroundings;
// tests swap it for a fake release source and a temp binary.
var selfUpdateHost = struct {
	source     func() update.Source
	executable func() (string, error)
	cliOnly    bool
}{
	source:     func() update.Source { return &update.GitHub{Repo: releaseRepo} },
	executable: os.Executable,
	cliOnly:    cliOnly,
}

// newSelfUpdateCmd is `dock self-update`: it replaces this CLI's binary
// with a release's dock-cli, by default the version the current context's
// server runs.
func newSelfUpdateCmd(g *globals) *cobra.Command {
	var (
		to             string
		latest, dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Install the CLI that matches your server",
		Long: `Replace this CLI with the release's dock-cli build for this machine,
checked against the release's SHA256SUMS and its signature (releases
from ` + update.FirstSignedRelease + ` on are signed; older ones are installed
with a warning). By default the release is the
version the current context's server runs, so the CLI and the server
match; with no server reachable, the latest release. --latest takes the
newest release, --to a given one.

The binary is replaced in place (symlinks followed) through a rename, so
a failed download leaves it as it was. When its directory isn't writable,
the error prints the command to run as root, with the version pinned.

This updates the CLI only; ` + "`dock update`" + ` updates the platform.`,
		Example: `  dock self-update
  dock self-update --latest
  dock self-update --to v0.54.1 --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if to != "" && latest {
				return &output.Error{Exit: output.ExitUsage, Message: "--to and --latest can't be used together"}
			}
			if to != "" && !isPublished(to) {
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("%q is not a release version", to), Hint: "Give a release tag, like `--to v0.54.1`."}
			}
			if !selfUpdateHost.cliOnly {
				return &output.Error{
					Exit:    output.ExitError,
					Message: "this dock is the server build, which self-update can't replace with the CLI-only build",
					Hint:    "Update a server with `dock update apply` (or the console's Updates page).",
				}
			}
			asset := update.CLIAsset(runtime.GOOS, runtime.GOARCH)
			if asset == "" {
				return &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("releases have no CLI for %s/%s", runtime.GOOS, runtime.GOARCH), Hint: "Build it from source: `make build-cli`."}
			}
			exe, err := selfUpdateHost.executable()
			if err == nil {
				exe, err = filepath.EvalSymlinks(exe)
			}
			if err != nil {
				return fmt.Errorf("find this CLI's binary: %w", err)
			}
			src := selfUpdateHost.source()
			v := selfUpdateView{From: version.Version, Asset: asset, Path: exe, DryRun: dryRun}
			if err := g.selfUpdateTarget(cmd.Context(), src, &v, to, latest); err != nil {
				return err
			}
			if v.From != v.To {
				if err := notHomebrew(exe, v.To); err != nil {
					return err
				}
				if err := writableDir(exe, v.To); err != nil {
					return err
				}
				if !dryRun {
					g.io.Progressf("Downloading %s %s", asset, v.To)
					signed, err := replaceBinary(cmd.Context(), src, v.To, asset, exe)
					if err != nil {
						return err
					}
					v.Updated, v.Unsigned = true, !signed
					if v.Unsigned {
						g.io.Warnf("dock %s predates signed releases (%s): it was checked against its SHA256SUMS only.", v.To, update.FirstSignedRelease)
					}
				}
			}
			return g.io.Print(v, func() error {
				switch {
				case v.From == v.To:
					g.io.Successf("dock is already %s%s", v.To, v.why())
				case dryRun:
					g.io.Successf("Would update dock from %s to %s%s: %s into %s", v.From, v.To, v.why(), asset, exe)
				default:
					g.io.Successf("Updated dock from %s to %s%s", v.From, v.To, v.why())
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "install this release, e.g. v0.54.1")
	cmd.Flags().BoolVar(&latest, "latest", false, "install the newest release instead of the server's version")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be installed, and where, without downloading it")
	return cmd
}

// why is the human reason for v.To, after the version.
func (v selfUpdateView) why() string {
	switch v.Target {
	case "server":
		return ", the version of " + v.Server
	case "latest":
		return ", the latest release"
	}
	return ""
}

// selfUpdateTarget fills in v.To and how it was picked: to when given,
// else the current context's server's version unless latest, else (or
// when no server answers, with a warning) the newest release that isn't
// a prerelease.
func (g *globals) selfUpdateTarget(ctx context.Context, src update.Source, v *selfUpdateView, to string, latest bool) error {
	if to != "" {
		v.To, v.Target = to, "pinned"
		return nil
	}
	if !latest {
		server, sv, err := contextServerVersion(ctx)
		switch {
		case err == nil && isPublished(sv):
			v.To, v.Target, v.Server = sv, "server", server
			return nil
		case err == nil:
			return &output.Error{
				Exit:    output.ExitError,
				Message: fmt.Sprintf("%s runs %s, a development build with no release to install", server, sv),
				Hint:    "Build the CLI from the server's commit (`make build-cli`), or take a release: `dock self-update --latest`.",
			}
		case errors.Is(err, clientconfig.ErrNoContext):
			v.ServerError = "no server is configured"
		default:
			v.ServerError = err.Error()
		}
		g.io.Warnf("Could not get the server's version (%s); taking the latest release.", v.ServerError)
	}
	releases, err := src.Releases(ctx)
	if err != nil {
		return &output.Error{Exit: output.ExitUnreachable, Message: fmt.Sprintf("could not list Dock's releases: %v", err), Hint: "Check this machine's network and try again, or pin one with `--to`.", Err: err}
	}
	for _, r := range releases {
		if !r.Prerelease && isPublished(r.Version) {
			v.To, v.Target = r.Version, "latest"
			return nil
		}
	}
	return &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("%s has no release to install", src)}
}

// contextServerVersion asks the current context's server (or DOCK_URL's)
// for its version; clientconfig.ErrNoContext when none is configured.
func contextServerVersion(ctx context.Context) (server, ver string, err error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", "", err
	}
	_, link := cfg.currentLink()
	r, err := cfg.Resolve(link)
	if err != nil {
		return "", "", err
	}
	ver, err = serverVersion(ctx, r.Server)
	return r.Server, ver, err
}

// writableDir fails, naming the root command that does the update with
// version pinned (root has no CLI context), when the directory of exe
// can't take a new file. It leaves nothing behind.
func writableDir(exe, version string) error {
	f, err := os.CreateTemp(filepath.Dir(exe), ".dock-self-update-*")
	if err != nil {
		return unwritable(exe, version, err)
	}
	_ = f.Close()
	return os.Remove(f.Name())
}

// notHomebrew refuses to replace a binary Homebrew installed (it lives in
// a Cellar): Homebrew would no longer know which version it has.
func notHomebrew(exe, version string) error {
	if !strings.Contains(filepath.ToSlash(exe), "/Cellar/dock/") {
		return nil
	}
	return &output.Error{
		Exit:    output.ExitError,
		Message: "this dock was installed with Homebrew, which updates it",
		Hint:    fmt.Sprintf("Run `brew upgrade dock` for the latest release. For %s exactly: `brew uninstall dock`, then `curl -fsSL https://dock.nordtempo.com/cli.sh | DOCK_VERSION=%s sh`.", version, version),
	}
}

// unwritable is the error for a binary in a directory this user can't
// write to.
func unwritable(exe, version string, err error) error {
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return &output.Error{
		Exit:    output.ExitError,
		Message: fmt.Sprintf("can't write to %s, where dock is installed", filepath.Dir(exe)),
		Hint:    fmt.Sprintf("Run it as root: `sudo %s self-update --to %s`.", exe, version),
		Err:     err,
	}
}

// replaceBinary downloads release version's asset next to exe, makes it
// executable and renames it over exe. Until the rename exe is untouched;
// a download that fails or doesn't match the signed SHA256SUMS is
// removed. signed is false for a release before signing began.
func replaceBinary(ctx context.Context, src update.Source, version, asset, exe string) (signed bool, err error) {
	f, err := os.CreateTemp(filepath.Dir(exe), ".dock-self-update-*")
	if err != nil {
		return false, unwritable(exe, version, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // a no-op after the rename
	signed, err = update.Fetch(ctx, src, version, asset, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case errors.Is(err, update.ErrUnsigned) || errors.Is(err, update.ErrBadSignature):
		return false, &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("refusing %s %s: %v", asset, version, err), Hint: "dock is unchanged. Report it: https://github.com/" + releaseRepo + "/security", Err: err}
	case err != nil:
		return false, &output.Error{Exit: output.ExitError, Message: fmt.Sprintf("could not download %s %s: %v", asset, version, err), Hint: "dock is unchanged. Try again, or pin another release with `--to`.", Err: err}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return false, err
	}
	return signed, os.Rename(tmp, exe)
}
