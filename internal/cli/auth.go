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

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/version"
)

// loginView is the JSON shape of `dock login`.
type loginView struct {
	Context string      `json:"context"`
	Server  string      `json:"server"`
	Me      apitypes.Me `json:"me"`
	// User is who approved a browser login.
	User          string           `json:"user,omitempty"`
	ServerVersion apitypes.Version `json:"serverVersion"`
}

func newLoginCmd(g *globals) *cobra.Command {
	var token, name string
	var tokenStdin, noBrowser bool
	cmd := &cobra.Command{
		Use:   "login [server-url]",
		Short: "Log in to a server in the browser, or with an API token",
		Long: `Log in, store the credentials as a context and make that context the
current one. Logging in again to the same server replaces its token. The
context is named after the server's host unless --name is given; without a
URL, log in again to the current context's server.

On a terminal, login happens in the browser: the CLI prints a link and a
code and opens the link. Sign in there, type the code and approve. The CLI
then gets its own token, with your role as scope, valid for 90 days. It
acts for you: it ends when you are disabled or deleted or your password
changes, and never has more access than your current role. ` + "`dock logout`" + `
revokes it. --no-browser only prints the link, for when
the browser is on another machine; it also works without a terminal.

Agents, CI and scripts use API tokens instead: pipe one in with
--token-stdin. --token works too, but leaves the token in shell history and
the process list. Create the first token on the server with
` + "`dock admin bootstrap`" + `.`,
		Example: `  dock login https://dock.example.com
  dock login https://dock.example.com --no-browser
  dock admin bootstrap | dock login http://127.0.0.1:7070 --token-stdin`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			var arg string
			if len(args) == 1 {
				arg = args[0]
			} else if r, err := cfg.resolve(); err == nil {
				arg = r.Server
			} else {
				return usageError("no server URL given", "Pass the server's URL: `dock login https://dock.example.com`.")
			}
			u, err := apiclient.ParseServerURL(arg)
			if err != nil {
				return err
			}
			server := u.String()
			if name == "" {
				if name, err = contextNameFor(cfg, u); err != nil {
					return err
				}
			} else if !contextName.MatchString(name) {
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("invalid context name %q", name), Hint: "Use letters, digits, '.', '_' and '-'."}
			}
			// No token flag: log in in the browser, on a terminal or when
			// asked to print the link (--no-browser).
			browser := token == "" && !tokenStdin && (g.io.Interactive || noBrowser)
			if !browser {
				if token, err = g.loginToken(token, tokenStdin); err != nil {
					return err
				}
			}

			ctx := cmd.Context()
			anon, err := apiclient.New(server, "")
			if err != nil {
				return err
			}
			ver, err := apiclient.Get[apitypes.Version](ctx, anon, apiPrefix+"/version")
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
			var approved apitypes.CLILoginWait
			if browser {
				if approved, err = g.browserLogin(ctx, server, !noBrowser); err != nil {
					return err
				}
				token = approved.Token
			}
			api, err := apiclient.New(server, token)
			if err != nil {
				return err
			}
			me, err := apiclient.Get[apitypes.Me](ctx, api, apiPrefix+"/me")
			if err != nil {
				if e, ok := errors.AsType[*output.Error](err); ok && e.Exit == output.ExitAuth && !browser {
					e.Hint = "Check the token. Admins create tokens with `dock token create`, or `dock admin bootstrap` on the server."
				}
				return err
			}

			// A token from an earlier browser login here is replaced: revoke
			// it rather than leave it valid for months.
			if old := cfg.Contexts[name]; old.LoginToken && old.Token != "" && old.Token != token && old.Server == server {
				if err := revokeOwnToken(ctx, server, old.Token); err != nil {
					g.io.Warnf("Could not revoke the previous login's token: %v", err)
				}
			}
			cfg.Contexts[name] = clientconfig.Context{Server: server, Token: token, LoginToken: browser}
			cfg.CurrentContext = name
			if err := cfg.save(); err != nil {
				return err
			}
			v := loginView{Context: name, Server: server, Me: me, User: approved.User, ServerVersion: ver}
			return g.io.Print(v, func() error {
				if browser {
					g.io.Successf("Logged in as %s (%s) on %s", approved.User, approved.Scope, server)
				} else {
					g.io.Successf("Logged in to %s as %s (%s)", server, me.Name, describeMe(me))
				}
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
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "log in in the browser, but only print the link (works without a terminal)")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API token from stdin (preferred for scripts)")
	cmd.Flags().StringVar(&token, "token", "", "API token; prefer --token-stdin, this ends up in shell history")
	cmd.Flags().StringVar(&name, "name", "", "context name (default: the server's host)")
	return cmd
}

// maxTokenInput bounds what --token-stdin reads.
const maxTokenInput = 4 << 10

// loginToken returns the token from --token or stdin (--token-stdin).
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
	return "", usageError("no API token given and no terminal to log in on",
		"Pipe a token in with --token-stdin, e.g. `dock login <url> --token-stdin < token-file`, "+
			"or log in in the browser: run `dock login` on a terminal, or pass --no-browser to only print the link.")
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
	if me.Scope == "" {
		s = me.Kind + ", no organization chosen (see `dock org list`)"
	}
	if me.Operator {
		s += ", platform operator"
	}
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
					"organization", me.Org,
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
from the config file. A token from a browser login is also revoked on the
server; tokens given with --token or --token-stdin stay valid, since they
may be in use elsewhere. The context itself is kept. Logging out again is
not an error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			revoked := false
			if hadToken && ctx.LoginToken {
				if err := revokeOwnToken(cmd.Context(), ctx.Server, ctx.Token); err != nil {
					g.io.Warnf("Could not revoke the token on %s (%v). It expires on its own; revoke it on your account page.", ctx.Server, err)
				} else {
					revoked = true
				}
			}
			ctx.Token, ctx.LoginToken = "", false
			cfg.Contexts[r.Name] = ctx
			if err := cfg.save(); err != nil {
				return err
			}
			v := struct {
				Context  string `json:"context"`
				HadToken bool   `json:"hadToken"`
				Revoked  bool   `json:"revoked"`
			}{r.Name, hadToken, revoked}
			return g.io.Print(v, func() error {
				switch {
				case revoked:
					g.io.Successf("Logged out of %s and revoked its token", r.Name)
				case hadToken:
					g.io.Successf("Logged out of %s", r.Name)
				default:
					g.io.Successf("Already logged out of %s", r.Name)
				}
				return nil
			})
		},
	}
}
