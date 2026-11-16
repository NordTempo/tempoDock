//go:build !dockcli

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"
	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/registry"
	"github.com/NordTempo/tempoDock/internal/registry/gateway"
)

// registrySigner loads (or, the first time, creates) the control plane's
// registry signing key in the data dir. `dock serve` mints tokens with it
// and `dock admin registry-config` writes the registry's config for it, so
// both must agree on the registry.Config built here.
func registrySigner(cfg config.Config) (*registry.Signer, error) {
	if !cfg.Registry.Enabled {
		return nil, errors.New("the registry is off on this server; set [registry] enabled = true (install.sh: REGISTRY=1)")
	}
	key, err := registry.LoadOrCreateKey(filepath.Join(cfg.DataDir, registry.KeyFile))
	if err != nil {
		return nil, err
	}
	return registry.NewSigner(key, registry.Config{
		Service: cfg.Registry.PublicName,
		Realm:   "http://" + cfg.InternalListen + api.RegistryTokenPath,
	}), nil
}

// platformRegistry is core.Registry on the control plane. Its image host
// is [registry] listen: the control plane reaches the registry container
// there, and every node serves it at the same address over the link, so
// image references work on every server. It opens the storage gateway's
// spool (ADR 0084); the caller serves the gateway on [registry]
// gateway_listen and closes it. onBucket hears its state changes.
func platformRegistry(cfg config.Config, s *registry.Signer, log *slog.Logger, onBucket func(*apitypes.RegistryBucket)) (*registry.Platform, error) {
	docker, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	access, secret := registry.GatewayKeys(s)
	gw, err := gateway.Open(gateway.Options{
		Dir: cfg.RegistrySpoolDir(), MaxBytes: int64(cfg.Registry.SpoolMaxGB) << 30,
		AccessKey: access, Secret: secret, Log: log.With("component", "registry-gateway"),
		OnChange: func(st gateway.Status) { onBucket(registry.BucketStatus(st)) },
	})
	if err != nil {
		return nil, err
	}
	return &registry.Platform{
		Name:        cfg.Registry.Listen,
		Signer:      s,
		Client:      &registry.Client{URL: "http://" + cfg.Registry.Listen, Signer: s},
		Docker:      docker,
		StorageDir:  cfg.Registry.StorageDir,
		ConfigDir:   cfg.Registry.ConfigDir,
		Listen:      cfg.Registry.Listen,
		Gateway:     gw,
		GatewayAddr: cfg.Registry.GatewayListen,
	}, nil
}

// newRegistryConfigCmd writes the registry container's config.yml and
// jwks.json. install.sh runs it as the dock user (it reads the signing
// key and the database) on every run and recreates the container only
// when they changed.
func newRegistryConfigCmd() *cobra.Command {
	configPath := config.DefaultPath
	var out string
	cmd := &cobra.Command{
		Use:   "registry-config --out <dir>",
		Short: "Write the image registry's config.yml and jwks.json",
		Long: `Write the registry container's config.yml and jwks.json into a directory.
Creates the control plane's registry signing key in the data dir on first
use. The output only depends on the key, the [registry] config and where
the registry keeps its images (the storage dir, or a storage connection's
bucket), so running it again writes the same files. config.yml holds the
bucket's keys: it is written 0640.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return usageError("--out is required", "Pass the directory to write config.yml and jwks.json to.")
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			s, err := registrySigner(cfg)
			if err != nil {
				return err
			}
			c, closeState, err := adminCore(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer closeState()
			bucket, err := c.RegistryBucket(cmd.Context())
			if err != nil {
				return err
			}
			// install.sh runs the registry on the host's network (ADR 0084),
			// reaching a bucket through the daemon's gateway.
			st := registry.Storage{Bucket: bucket, Listen: cfg.Registry.Listen, Gateway: cfg.Registry.GatewayListen}
			if _, err := registry.WriteConfig(out, s, st); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Wrote %s and %s\n", filepath.Join(out, "config.yml"), filepath.Join(out, "jwks.json"))
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	cmd.Flags().StringVar(&out, "out", "", "directory to write config.yml and jwks.json to")
	return cmd
}

// newRegistryStorageCmd points a new registry at a bucket: install.sh runs
// it (as the dock user, before starting the daemon) when the installer
// chooses S3-compatible storage. Moving images that are already stored is
// `dock registry migrate`.
func newRegistryStorageCmd() *cobra.Command {
	configPath := config.DefaultPath
	var req apitypes.CreateStorageRequest
	pathStyle := true
	cmd := &cobra.Command{
		Use:   "registry-storage <name> --endpoint <url> --bucket <bucket> < keys",
		Short: "Keep a new registry's images in an S3-compatible bucket",
		Long: `Save the storage connection <name> (or update it) and keep the registry's
images in its bucket, under dock-registry/. Reads the access key ID and
the secret access key from stdin, one per line. Dock lists the bucket
first, so wrong keys or a wrong endpoint fail here. The daemon applies it
on its next start; the registry's config follows from ` + "`dock admin registry-config`" + `.

For a registry with no images yet: install.sh checks the storage dir is
empty. Images already stored move with ` + "`dock registry migrate`" + `.`,
		Example: `  printf '%s\n%s\n' "$KEY_ID" "$SECRET" | dock admin registry-storage hetzner \
      --endpoint https://fsn1.your-objectstorage.com --region fsn1 --bucket dock-images`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxSecretInput))
			if err != nil {
				return err
			}
			id, secret, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
			req.AccessKeyID, req.SecretAccessKey = strings.TrimSpace(id), strings.TrimSpace(secret)
			if req.AccessKeyID == "" || req.SecretAccessKey == "" {
				return usageError("expected the access key ID and the secret access key on stdin, one per line", "printf '%s\\n%s\\n' \"$KEY_ID\" \"$SECRET\" | dock admin registry-storage ...")
			}
			req.Name, req.PathStyle = args[0], &pathStyle
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if !cfg.Registry.Enabled {
				return usageError("the registry is off on this server", "Set [registry] enabled = true (install.sh: REGISTRY=1).")
			}
			c, closeState, err := adminCore(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer closeState()
			if _, err := c.InstallRegistryStorage(cmd.Context(), core.System, req); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "The registry keeps its images in bucket %s at %s (storage %s)\n", req.Bucket, req.Endpoint, req.Name)
			return err
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&configPath, "config", configPath, "path to the daemon config file")
	fs.StringVar(&req.Endpoint, "endpoint", "", "the store's base URL, like https://fsn1.your-objectstorage.com")
	fs.StringVar(&req.Region, "region", "", "region to sign requests for (default us-east-1)")
	fs.StringVar(&req.Bucket, "bucket", "", "the bucket")
	fs.BoolVar(&pathStyle, "path-style", pathStyle, "address the bucket in the URL's path; false puts it in the host name")
	return cmd
}

// adminCore opens the state and a Core without a runtime, for admin
// commands that run next to the daemon. close closes the state.
func adminCore(ctx context.Context, cfg config.Config) (c *core.Core, closeState func(), err error) {
	st, box, err := openState(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	c, err = core.New(ctx, core.Options{Config: cfg, Store: st, Secrets: box})
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	return c, func() { _ = st.Close() }, nil
}
