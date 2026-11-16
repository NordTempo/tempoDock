package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The platform registry (ADR 0014 section 4): its size, pruning, where it
// keeps its images (ADR 0034), and the services keeping images in it.
// Also the org's credentials for private registries (ADR 0051).

func newRegistryCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "The image registry: storage, size, pruning and the services keeping images (admin)",
		Long: `Show the platform registry: where it keeps its images (this server's disk or
a storage connection's bucket), their size, how the last prune and migration
went, and every service with images in it, with how many its rollback
window keeps. Prune and migration state are since the daemon started.

With the images in a bucket, pushes land on this server's disk first and
upload in the background (ADR 0084): it also shows the bucket's health
from recent calls, what waits to upload, the spool's size and blobs the
bucket keeps timing out on.

Images deployed from private registries (ghcr.io, a registry of your own,
Docker Hub) are pulled with the organization's credentials for their host:
` + "`dock registry login`" + ` adds them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := get[apitypes.Registry](cmd.Context(), s, apiPrefix+"/registry")
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error { return printRegistry(g, r) })
		},
	}
	cmd.AddCommand(newRegistryPruneCmd(g), newRegistryMigrateCmd(g),
		newRegistryLoginCmd(g), newRegistryLogoutCmd(g), newRegistryCredentialsCmd(g))
	return cmd
}

func printRegistry(g *globals, r apitypes.Registry) error {
	if !r.Enabled {
		g.io.Detail("This Dock has no registry; images stay on the server that built them.")
		return nil
	}
	prune := "not since the daemon started"
	if r.Pruning {
		prune = "running"
	} else if r.LastPruneAt != nil {
		prune = ago(*r.LastPruneAt) + ", " + plural(r.LastPruned, "image") + " deleted"
	}
	storage := "this server's disk"
	if r.Storage != "local" && r.Storage != "" {
		storage = "storage " + r.Storage
	}
	if err := g.io.Fields(
		"host", r.Host,
		"storage", storage,
		"stored", bytesize(r.StoredBytes),
		"rollback window", plural(r.KeepReleases, "release"),
		"last prune", prune,
		"error", r.LastPruneError,
	); err != nil {
		return err
	}
	if m := r.Migration; m != nil {
		switch {
		case m.Running:
			g.io.Warnf("Moving the images from %s to %s since %s ago; builds and pulls wait for it.", m.From, m.To, ago(m.StartedAt))
		case m.Error != "":
			g.io.Warnf("Moving the images from %s to %s failed: %s", m.From, m.To, m.Error)
		default:
			g.io.Detail(fmt.Sprintf("Moved the images from %s to %s: %d objects, %s.", m.From, m.To, m.Objects, bytesize(m.Bytes)))
		}
	}
	if r.CollectPending {
		g.io.Warnf("Deleted images' layers are still on disk; the next prune collects them.")
	}
	if b := r.Bucket; b != nil {
		if err := printRegistryBucket(g, *b); err != nil {
			return err
		}
	}
	if len(r.Services) == 0 {
		g.io.Detail("No images pushed yet.")
		return nil
	}
	t := g.io.Table("service", "repository", "kept", "newest", "active")
	for _, s := range r.Services {
		kept := strconv.Itoa(s.Kept)
		if s.Archived {
			kept = "archived"
		}
		active := "-"
		if s.Active != 0 {
			active = "#" + itoa(s.Active)
		}
		t.Row(s.Project+"/"+s.Environment+"/"+s.Service, s.Repository, kept, "#"+itoa(s.Newest), active)
	}
	return t.Flush()
}

// printRegistryBucket shows the bucket's health and the spool pushes land
// in (ADR 0084): one line when all is well.
func printRegistryBucket(g *globals, b apitypes.RegistryBucket) error {
	if b.State == "healthy" && b.Refusing == "" && b.Waiting == 0 && len(b.Stuck) == 0 {
		g.io.Detail("Bucket healthy, nothing waiting to upload.")
		return nil
	}
	waiting := "nothing"
	if b.Waiting > 0 {
		waiting = fmt.Sprintf("%s, %s", plural(b.Waiting, "object"), bytesize(b.WaitingBytes))
		if b.OldestWaitingAt != nil {
			waiting += ", the oldest since " + ago(*b.OldestWaitingAt)
		}
	}
	pushes := "accepted; they land in the spool and upload in the background"
	switch b.Refusing {
	case "spool":
		pushes = "refused: the spool is full; builds fail at the push until uploads free space"
	case "disk":
		pushes = fmt.Sprintf("refused: less than %d%% of the disk would stay free", b.DiskMinFreePercent)
	}
	var stuck []string
	for _, s := range b.Stuck {
		stuck = append(stuck, s.Digest+" since "+ago(s.Since))
	}
	if err := g.io.Fields(
		"bucket", fmt.Sprintf("%s since %s: %d of the last %d calls failed or took over 10 s, the slowest %s", b.State, ago(b.Since), b.Bad, b.Calls, fmtMillis(b.SlowestMS)),
		"last error", b.LastError,
		"waiting to upload", waiting,
		"spool", bytesize(b.SpoolBytes)+" of "+bytesize(b.SpoolMaxBytes),
		"disk", bytesize(b.DiskFreeBytes)+" free of "+bytesize(b.DiskBytes),
		"pushes", pushes,
		"stuck blobs", strings.Join(stuck, ", "),
	); err != nil {
		return err
	}
	if b.Waiting > 0 {
		g.io.Detail("What waits in the spool is only on this server's disk until it uploads.")
	}
	if len(b.Stuck) > 0 {
		g.io.Detail("The bucket keeps timing out on the stuck blobs; the next push of an image with one uploads it again.")
	}
	return nil
}

// fmtMillis is a duration in milliseconds as people read it.
func fmtMillis(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + " ms"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + " s"
}

func newRegistryPruneCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Delete the images no release keeps, now (admin)",
		Long: `Delete the registry's images outside every service's rollback window that
no active release uses, and collect their layers. It runs in the
background; the registry stops for the seconds the collection takes.
Dock also prunes by itself, at most hourly, after deploys and archives.
Refused while builds or pulls use the registry.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.Registry](cmd.Context(), s, http.MethodPost, apiPrefix+"/registry/prune", nil)
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error {
				g.io.Successf("Pruning the registry")
				g.io.Detail("It finishes in the background; `dock registry` shows the result.")
				return nil
			})
		},
	}
}

// newRegistryMigrateCmd is `dock registry migrate`.
func newRegistryMigrateCmd(g *globals) *cobra.Command {
	var req apitypes.MigrateRegistryRequest
	var yes bool
	cmd := &cobra.Command{
		Use:   "migrate --to s3|local [--storage <name>]",
		Short: "Move the registry's images to a bucket or back to this server's disk (admin)",
		Long: `Move the registry's images to a storage connection's bucket (--to s3) or to
the control plane's disk (--to local). The registry serves them read-only
while they are copied; builds and pulls wait, like during a prune. Once the
copy is checked, the registry switches over and the old copy is deleted. A
failure before the switch leaves everything as it was.

The bucket must not hold registry data yet (dock-registry/ in it). --storage
may be left out when there is only one storage connection. It runs in the
background; ` + "`dock registry`" + ` shows how it went.`,
		Example: `  dock storage add hetzner ...      # once
  dock registry migrate --to s3 --storage hetzner
  dock registry migrate --to local`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if req.To != "s3" && req.To != "local" {
				return usageError("--to must be s3 or local", "Run `dock registry migrate --help`.")
			}
			if req.To == "local" && req.Storage != "" {
				return usageError("--storage goes with --to s3", "Leave out --storage to move the images to this server's disk.")
			}
			if !yes {
				if err := g.io.Confirm("Move the registry's images? Builds and pulls wait until the copy is done.", "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			r, err := send[apitypes.Registry](cmd.Context(), s, http.MethodPost, apiPrefix+"/registry/migrate", req)
			if err != nil {
				return err
			}
			return g.io.Print(r, func() error {
				to := req.To
				if r.Migration != nil {
					to = r.Migration.To
				}
				g.io.Successf("Moving the registry's images to %s", to)
				g.io.Detail("It finishes in the background; `dock registry` shows the result.")
				return nil
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&req.To, "to", "", "s3 (a storage connection) or local (this server's disk)")
	fs.StringVar(&req.Storage, "storage", "", "the storage connection, with --to s3")
	fs.BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// registryCredentialsPath is where the org's registry credentials are.
const registryCredentialsPath = apiPrefix + "/org/registry-credentials"

// newRegistryLoginCmd is `dock registry login`.
func newRegistryLoginCmd(g *globals) *cobra.Command {
	var req apitypes.SetRegistryCredentialRequest
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "login <host> --username <name> [--password-stdin]",
		Short: "Save the credentials deploys pull a private registry's images with (admin)",
		Long: `Save a username and password (or access token) for a private image registry.
Deploys of the organization's images from that host pull with them, from
the next pull on; images from other hosts are still pulled anonymously.
The host is as in an image reference: ghcr.io, registry.example.com:5000,
or docker.io for Docker Hub. Logging in again replaces the credentials.

The password is read from stdin with --password-stdin, or asked for on a
terminal; it never goes in an argument. It is stored encrypted and never
shown again. Builds don't use it: a Dockerfile's FROM image must be public.`,
		Example: `  printf %s "$GHCR_TOKEN" | dock registry login ghcr.io --username acme-bot --password-stdin
  dock registry login registry.example.com:5000 --username deploy`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if req.Username == "" {
				return usageError("--username is required", "Pass the registry account's name, e.g. `--username acme-bot`.")
			}
			var err error
			if passwordStdin {
				req.Password, err = readRegistryPassword(g)
			} else {
				req.Password, err = g.io.PromptSecret("Password or token", "password-stdin")
			}
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			rc, err := send[apitypes.RegistryCredential](cmd.Context(), s, http.MethodPut, registryCredentialsPath+"/"+url.PathEscape(args[0]), req)
			if err != nil {
				return err
			}
			return g.io.Print(rc, func() error {
				g.io.Successf("Saved the credentials for %s (user %s)", rc.Host, rc.Username)
				g.io.Detail("Deploys pull images from " + rc.Host + " with them from the next pull on.")
				return nil
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVarP(&req.Username, "username", "u", "", "the registry account's name")
	fs.BoolVar(&passwordStdin, "password-stdin", false, "read the password or token from stdin")
	return cmd
}

// maxRegistryPassword bounds what --password-stdin reads: a token, or a
// JSON key some registries take as the password (as core allows).
const maxRegistryPassword = 16 << 10

// readRegistryPassword reads --password-stdin's password: all of stdin
// but a final line break, as `docker login --password-stdin` does.
func readRegistryPassword(g *globals) (string, error) {
	b, err := io.ReadAll(io.LimitReader(g.io.In, maxRegistryPassword+1))
	if err != nil {
		return "", err
	}
	pw := strings.TrimRight(string(b), "\r\n")
	switch {
	case len(b) > maxRegistryPassword:
		return "", usageError("the password on stdin is longer than 16 KB", "Pipe just the password or token.")
	case pw == "":
		return "", usageError("--password-stdin read nothing", "Pipe the password or token in: `printf %s \"$TOKEN\" | dock registry login HOST --username NAME --password-stdin`.")
	}
	return pw, nil
}

// newRegistryLogoutCmd is `dock registry logout`.
func newRegistryLogoutCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "logout <host>",
		Short: "Remove the credentials for a private registry (admin)",
		Long:  `Remove the organization's credentials for a registry host. Pulls from it are anonymous again, so deploys of its private images fail. Running services are left alone.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, registryCredentialsPath+"/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]string{"removed": args[0]}, func() error {
				g.io.Successf("Removed the credentials for %s", args[0])
				return nil
			})
		},
	}
}

// newRegistryCredentialsCmd is `dock registry credentials`.
func newRegistryCredentialsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "credentials",
		Short: "List the private registries the organization signs in to (admin)",
		Long:  `List the organization's registry credentials: host, username and when they were last set. Passwords are never shown.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			list, err := get[[]apitypes.RegistryCredential](cmd.Context(), s, registryCredentialsPath)
			if err != nil {
				return err
			}
			return g.io.Print(list, func() error {
				if len(list) == 0 {
					g.io.Detail("No registry credentials; images are pulled anonymously. Add some with `dock registry login`.")
					return nil
				}
				t := g.io.Table("host", "username", "updated", "by")
				for _, rc := range list {
					t.Row(rc.Host, rc.Username, ago(rc.UpdatedAt), rc.UpdatedBy)
				}
				return t.Flush()
			})
		},
	}
}
