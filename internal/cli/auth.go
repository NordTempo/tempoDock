package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/cli/output"
	"github.com/NordTempo/platform/internal/clientconfig"
)

func newLogoutCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the token of the context in use from this machine",
		Long: `Remove the token of the context in use (see ` + "`platform context current`" + `)
from the config file. The context itself is kept. Logging out again is not an error.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			r, err := cfg.resolve()
			if err != nil {
				return err
			}
			if r.Source == clientconfig.SourceEnvURL {
				return &output.Error{
					Exit:    output.ExitUsage,
					Message: "credentials come from " + clientconfig.EnvURL + " and " + clientconfig.EnvToken + ", not the config file",
					Hint:    "Unset " + clientconfig.EnvToken + " to stop using the token.",
				}
			}
			ctx := cfg.Contexts[r.Name]
			hadToken := ctx.Token != ""
			ctx.Token = ""
			cfg.Contexts[r.Name] = ctx
			if err := cfg.save(); err != nil {
				return err
			}
			v := struct {
				Context  string `json:"context"`
				HadToken bool   `json:"hadToken"`
			}{r.Name, hadToken}
			return g.io.Print(v, func() error {
				if hadToken {
					g.io.Successf("Logged out of %s", r.Name)
				} else {
					g.io.Successf("Already logged out of %s", r.Name)
				}
				if os.Getenv(clientconfig.EnvToken) != "" {
					g.io.Warnf("%s is still set and will be used.", clientconfig.EnvToken)
				}
				return nil
			})
		},
	}
}
