package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/cli/apiclient"
	"github.com/NordTempo/platform/internal/cli/output"
	"github.com/NordTempo/platform/internal/clientconfig"
	"github.com/NordTempo/platform/internal/version"
)

// loginView is the JSON shape of `dock login`.
type loginView struct {
	Context       string           `json:"context"`
	Server        string           `json:"server"`
	Me            apitypes.Me      `json:"me"`
	ServerVersion apitypes.Version `json:"serverVersion"`
}

func newLoginCmd(g *globals) *cobra.Command {
	var token, name string
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "login <server-url>",
		Short: "Log in to a server with an API token",
		Long: `Verify the token against the server, store it as a context and make that
context the current one. Logging in again to the same server replaces its
token. The context is named after the server's host unless --name is given.

On a terminal the token is prompted for without echo. Scripts should pipe it
in with --token-stdin. --token works too, but leaves the token in shell
history and the process list. Create the first token on the server with
` + "`dock admin bootstrap`" + `.`,
		Example: `  dock login https://dock.example.com
  dock admin bootstrap | dock login http://127.0.0.1:7070 --token-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := apiclient.ParseServerURL(args[0])
			if err != nil {
				return err
			}
			server := u.String()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if name == "" {
				if name, err = contextNameFor(cfg, u); err != nil {
					return err
				}
			} else if !contextName.MatchString(name) {
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("invalid context name %q", name), Hint: "Use letters, digits, '.', '_' and '-'."}
			}
			if token, err = g.loginToken(token, tokenStdin); err != nil {
				return err
			}

			api, err := apiclient.New(server, token)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			ver, err := apiclient.Get[apitypes.Version](ctx, api, apiPrefix+"/version")
			if err != nil {
				return err
			}
			if ver.APIVersion != apitypes.APIVersion {
				return &output.Error{
					Exit:    output.ExitIncompatible,
					Message: fmt.Sprintf("server speaks API %s, this CLI speaks %s", ver.APIVersion, apitypes.APIVersion),
					Hint:    "Install the CLI that matches the server (" + ver.Version + ").",
				}
			}
			me, err := apiclient.Get[apitypes.Me](ctx, api, apiPrefix+"/me")
			if err != nil {
				if e, ok := errors.AsType[*output.Error](err); ok && e.Exit == output.ExitAuth {
					e.Hint = "Check the token. Admins create tokens with `dock token create`, or `dock admin bootstrap` on the server."
				}
				return err
			}

			cfg.Contexts[name] = clientconfig.Context{Server: server, Token: token}
			cfg.CurrentContext = name
			if err := cfg.save(); err != nil {
				return err
			}
			v := loginView{Context: name, Server: server, Me: me, ServerVersion: ver}
			return g.io.Print(v, func() error {
				g.io.Successf("Logged in to %s as %s (%s)", server, me.Name, describeMe(me))
				g.io.Successf("Now using context %s", name)
				if ver.Version != version.Version {
					g.io.Warnf("The server runs %s and this CLI is %s. Keep them in step to avoid surprises.", ver.Version, version.Version)
				}
				if os.Getenv(clientconfig.EnvURL) != "" || os.Getenv(clientconfig.EnvContext) != "" {
					g.io.Warnf("%s or %s is set and takes precedence over the current context.", clientconfig.EnvURL, clientconfig.EnvContext)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API token from stdin (preferred for scripts)")
	cmd.Flags().StringVar(&token, "token", "", "API token; prefer --token-stdin, this ends up in shell history")
	cmd.Flags().StringVar(&name, "name", "", "context name (default: the server's host)")
	return cmd
}

// maxTokenInput bounds what --token-stdin reads.
const maxTokenInput = 4 << 10

// loginToken returns the token from --token, stdin (--token-stdin) or a
// hidden prompt on a terminal, in that order of the flags given.
func (g *globals) loginToken(flag string, fromStdin bool) (string, error) {
	switch {
	case flag != "" && fromStdin:
		return "", usageError("pass --token or --token-stdin, not both", "Prefer --token-stdin: `dock login <url> --token-stdin < token-file`.")
	case flag != "":
		return flag, nil
	case fromStdin:
		b, err := io.ReadAll(io.LimitReader(g.io.In, maxTokenInput+1))
		if err != nil {
			return "", err
		}
		token := strings.TrimSpace(string(b))
		if token == "" || len(b) > maxTokenInput || strings.ContainsAny(token, " \t\r\n") {
			return "", usageError("--token-stdin expects exactly one token on stdin", "Pipe just the token: `printf %s \"$TOKEN\" | dock login <url> --token-stdin`.")
		}
		return token, nil
	}
	if !g.io.Interactive {
		return "", usageError("no API token given", "Pipe it in with --token-stdin, e.g. `dock login <url> --token-stdin < token-file`. The CLI only prompts on a terminal.")
	}
	return g.io.PromptSecret("API token", "token-stdin")
}

// contextNameFor picks the context for a login to u: the context that
// already points at u, else one named after its host. A different server
// already using that name is a conflict, never silently replaced.
func contextNameFor(cfg userConfig, u *url.URL) (string, error) {
	for name, c := range cfg.Contexts {
		if c.Server == u.String() {
			return name, nil
		}
	}
	name := strings.Trim(nonNameChars.ReplaceAllString(u.Hostname(), "-"), "-.")
	if name == "" {
		name = "default"
	}
	if c, ok := cfg.Contexts[name]; ok && c.Server != u.String() {
		return "", &output.Error{
			Exit:    output.ExitConflict,
			Message: fmt.Sprintf("context %q already points at %s", name, c.Server),
			Hint:    "Pass --name to pick another context name.",
		}
	}
	return name, nil
}

var nonNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// describeMe is "token, scope deploy, limited to shop/staging".
func describeMe(me apitypes.Me) string {
	s := me.Kind + ", scope " + string(me.Scope)
	if me.Project != "" {
		s += ", limited to " + me.Project
		if me.Environment != "" {
			s += "/" + me.Environment
		}
	}
	return s
}

func newWhoamiCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the token in use belongs to and what it may do",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			me, err := get[apitypes.Me](cmd.Context(), s, apiPrefix+"/me")
			if err != nil {
				return err
			}
			return g.io.Print(me, func() error {
				ctxName := s.server.Name
				if ctxName == "" {
					ctxName = "(" + clientconfig.EnvURL + ")"
				}
				return g.io.Fields(
					"name", me.Name,
					"email", me.Email,
					"access", describeMe(me),
					"server", s.server.Server,
					"context", ctxName,
				)
			})
		},
	}
}

func newLogoutCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the token of the context in use from this machine",
		Long: `Remove the token of the context in use (see ` + "`dock context current`" + `)
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
				return nil
			})
		},
	}
}
