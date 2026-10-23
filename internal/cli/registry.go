//go:build !dockcli

package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/moby/moby/client"
	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/registry"
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
// image references work on every server.
func platformRegistry(cfg config.Config, s *registry.Signer) (*registry.Platform, error) {
	docker, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &registry.Platform{
		Name:       cfg.Registry.Listen,
		Signer:     s,
		Client:     &registry.Client{URL: "http://" + cfg.Registry.Listen, Signer: s},
		Docker:     docker,
		StorageDir: cfg.Registry.StorageDir,
	}, nil
}

// newRegistryConfigCmd writes the registry container's config.yml and
// jwks.json. install.sh runs it as the dock user (it reads the signing
// key) on every run and recreates the container only when they changed.
func newRegistryConfigCmd() *cobra.Command {
	configPath := config.DefaultPath
	var out string
	cmd := &cobra.Command{
		Use:   "registry-config --out <dir>",
		Short: "Write the image registry's config.yml and jwks.json",
		Long: `Write the registry container's config.yml and jwks.json into a directory.
Creates the control plane's registry signing key in the data dir on first
use. The output only depends on the key and the [registry] config, so
running it again writes the same files.`,
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
			if _, err := registry.WriteConfig(out, s); err != nil {
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
