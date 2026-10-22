package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/version"
)

// releaseRepo is the GitHub repository releases are published to
// (.github/workflows/release.yml).
const releaseRepo = "NordTempo/tempoDock"

// versionView is the JSON shape of `dock version`.
type versionView struct {
	CLI string `json:"cli"`
	// Server is the configured server's URL; empty when none is.
	Server        string `json:"server,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`
	// ServerError is why the server's version is unknown (unreachable, ...).
	ServerError string `json:"serverError,omitempty"`
	// Hint is how to get the CLI in step when the versions differ.
	Hint string `json:"hint,omitempty"`
}

// newVersionCmd is `dock version`: this CLI's version and, when a server is
// configured, the server's, with one request to its anonymous version
// endpoint. It never fails because the server is down.
func newVersionCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the CLI's and the server's version",
		Long: `Show this CLI's version and, when a server is configured (a context or
DOCK_URL), the version it runs, with the command that installs the
matching CLI when they differ. Without a server, nothing is sent.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v := versionView{CLI: version.Version}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			_, link := cfg.currentLink()
			if r, err := cfg.Resolve(link); err == nil {
				v.Server = r.Server
				v.ServerVersion, err = serverVersion(cmd.Context(), r.Server)
				if err != nil {
					v.ServerError = err.Error()
				} else if v.ServerVersion != v.CLI {
					v.Hint = upgradeHint(v.ServerVersion)
				}
			} else if !errors.Is(err, clientconfig.ErrNoContext) {
				v.ServerError = err.Error()
			}
			return g.io.Print(v, func() error {
				server := v.ServerVersion
				if v.Server != "" && server == "" {
					server = "unknown"
				}
				if v.Server != "" {
					server += "  " + v.Server
				}
				if err := g.io.Fields("cli", v.CLI, "server", server); err != nil {
					return err
				}
				if v.ServerError != "" {
					g.io.Warnf("Could not get the server's version: %s", v.ServerError)
				}
				if v.Hint != "" {
					g.io.Warnf("The server runs %s and this CLI is %s. %s", v.ServerVersion, v.CLI, v.Hint)
				}
				return nil
			})
		},
	}
}

// serverVersion asks server for its version, without sending the token.
func serverVersion(ctx context.Context, server string) (string, error) {
	anon, err := apiclient.New(server, "")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ver, err := apiclient.Get[apitypes.Version](ctx, anon, apiPrefix+"/version")
	return ver.Version, err
}

// upgradeHint is one line that gets the CLI to serverVersion: the install
// command for a published release, or a build from source otherwise.
func upgradeHint(serverVersion string) string {
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`).MatchString(serverVersion) {
		return "The server isn't a release; build the CLI from its commit (make build-cli)."
	}
	return fmt.Sprintf("Install the matching CLI: gh release download %s -R %s -p install-cli.sh -O - | DOCK_VERSION=%s sh",
		serverVersion, releaseRepo, serverVersion)
}
