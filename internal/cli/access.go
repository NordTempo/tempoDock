package cli

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/apitypes"
)

// Admin commands: tokens, users, the audit log and servers.

func newTokenCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "token",
		Short: "Manage API tokens (admin)",
		Long: `API tokens for the CLI, CI and agents. A token has a scope (read < deploy <
admin) and can be limited to one project or one environment of it.`,
	})
	cmd.AddCommand(newTokenListCmd(g), newTokenCreateCmd(g), newTokenRevokeCmd(g))
	return cmd
}

func newTokenListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List tokens",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			tokens, err := get[[]apitypes.Token](cmd.Context(), s, apiPrefix+"/tokens")
			if err != nil {
				return err
			}
			return g.io.Print(tokens, func() error {
				t := g.io.Table("id", "name", "scope", "limited to", "status", "last used", "created by")
				for _, tk := range tokens {
					t.Row(itoa(tk.ID), tk.Name, string(tk.Scope), dash(tokenLimit(tk.Project, tk.Environment)), tokenStatus(tk), agoPtr(tk.LastUsedAt), tk.CreatedBy)
				}
				return t.Flush()
			})
		},
	}
}

func tokenLimit(project, env string) string {
	if env != "" {
		return project + "/" + env
	}
	return project
}

func tokenStatus(t apitypes.Token) string {
	switch {
	case t.RevokedAt != nil:
		return "revoked"
	case t.ExpiresAt == nil:
		return "active"
	case t.ExpiresAt.Before(time.Now()):
		return "expired"
	default:
		return "expires " + t.ExpiresAt.Local().Format("2006-01-02")
	}
}

func newTokenCreateCmd(g *globals) *cobra.Command {
	var scope, project, env, expires string
	cmd := &cobra.Command{
		Use:   "create <name> --scope read|deploy|admin",
		Short: "Create a token; its secret is shown once",
		Long: `Create a token. The secret is printed once (also with --json) and can't be
shown again. --project and --environment limit the token; they are never
taken from the directory's link.`,
		Example: `  dock token create ci --scope deploy --project shop --environment staging --expires 90d`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if scope == "" {
				var err error
				if scope, err = g.io.Choose("Scope", "scope", []string{"read", "deploy", "admin"}); err != nil {
					return err
				}
			}
			days, err := parseExpiry(expires)
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			req := apitypes.CreateTokenRequest{Name: args[0], Scope: apitypes.Scope(scope), Project: project, Environment: env, ExpiresInDays: days}
			ct, err := send[apitypes.CreatedToken](cmd.Context(), s, http.MethodPost, apiPrefix+"/tokens", req)
			if err != nil {
				return err
			}
			return g.io.Print(ct, func() error {
				limit := ""
				if l := tokenLimit(ct.Token.Project, ct.Token.Environment); l != "" {
					limit = ", limited to " + l
				}
				g.io.Successf("Created token %s (id %d, scope %s%s, %s)", ct.Token.Name, ct.Token.ID, ct.Token.Scope, limit, tokenStatus(ct.Token))
				_, _ = fmt.Fprintln(g.io.Out, ct.Secret)
				g.io.Warnf("Store the token now; it is never shown again.")
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "read, deploy or admin")
	cmd.Flags().StringVarP(&project, "project", "p", "", "limit the token to this project")
	cmd.Flags().StringVarP(&env, "environment", "e", "", "limit the token to this environment of --project")
	cmd.Flags().StringVar(&expires, "expires", "", "lifetime: days like 90d, or a duration like 720h (default: never)")
	return cmd
}

// parseExpiry turns "90d", "90" or a Go duration into whole days (rounded
// up). Empty means never.
func parseExpiry(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
		return n, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return int(math.Ceil(d.Hours() / 24)), nil
	}
	return 0, usageError(fmt.Sprintf("invalid --expires %q", s), "Use days like 90d, or a duration like 720h.")
}

func newTokenRevokeCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a token; it stops working immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return usageError(fmt.Sprintf("%q is not a token id", args[0]), "Token ids are shown by `dock token list`.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/tokens/"+itoa(id)); err != nil {
				return err
			}
			v := struct {
				ID      int64 `json:"id"`
				Revoked bool  `json:"revoked"`
			}{id, true}
			return g.io.Print(v, func() error {
				g.io.Successf("Revoked token %d", id)
				return nil
			})
		},
	}
}

func newUserCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "user", Short: "Manage users who log in to the web UI (admin)"})
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List users",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			users, err := get[[]apitypes.User](cmd.Context(), s, apiPrefix+"/users")
			if err != nil {
				return err
			}
			return g.io.Print(users, func() error {
				t := g.io.Table("id", "email", "name", "role", "created")
				for _, u := range users {
					t.Row(itoa(u.ID), u.Email, u.Name, string(u.Role), ago(u.CreatedAt))
				}
				return t.Flush()
			})
		},
	}

	var req apitypes.CreateUserRequest
	var role string
	create := &cobra.Command{
		Use:   "create --email <email> --name <name> --role read|deploy|admin",
		Short: "Create a user",
		Long:  "Create a user. Without --password, it is prompted for on a terminal (at least 12 characters).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			if req.Email == "" {
				if req.Email, err = g.io.Prompt("Email", "email"); err != nil {
					return err
				}
			}
			if req.Name == "" {
				if req.Name, err = g.io.Prompt("Name", "name"); err != nil {
					return err
				}
			}
			if role == "" {
				if role, err = g.io.Choose("Role", "role", []string{"read", "deploy", "admin"}); err != nil {
					return err
				}
			}
			req.Role = apitypes.Scope(role)
			if req.Password == "" {
				if req.Password, err = g.io.PromptSecret("Password", "password"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			u, err := send[apitypes.User](cmd.Context(), s, http.MethodPost, apiPrefix+"/users", req)
			if err != nil {
				return err
			}
			return g.io.Print(u, func() error {
				g.io.Successf("Created user %s <%s> with role %s", u.Name, u.Email, u.Role)
				return nil
			})
		},
	}
	create.Flags().StringVar(&req.Email, "email", "", "login email")
	create.Flags().StringVar(&req.Name, "name", "", "display name")
	create.Flags().StringVar(&role, "role", "", "read, deploy or admin")
	create.Flags().StringVar(&req.Password, "password", "", "password (prompted for on a terminal)")

	cmd.AddCommand(list, create)
	return cmd
}

func newAuditCmd(g *globals) *cobra.Command {
	var project, env, action string
	var limit int
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show the audit log, newest first",
		Long:  "Every mutating action with who did it. Filters are explicit flags; the directory's link is not used.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			q := url.Values{"limit": {strconv.Itoa(limit)}}
			for k, v := range map[string]string{"project": project, "environment": env, "action": action} {
				if v != "" {
					q.Set(k, v)
				}
			}
			events, err := get[[]apitypes.AuditEvent](cmd.Context(), s, apiPrefix+"/audit?"+q.Encode())
			if err != nil {
				return err
			}
			return g.io.Print(events, func() error {
				t := g.io.Table("time", "actor", "action", "target", "summary")
				for _, e := range events {
					t.Row(e.CreatedAt.Local().Format("2006-01-02 15:04:05"), e.Actor, e.Action, e.Target, e.Summary)
				}
				return t.Flush()
			})
		},
	}
	cmd.Flags().StringVarP(&project, "project", "p", "", "only this project")
	cmd.Flags().StringVarP(&env, "environment", "e", "", "only this environment of --project")
	cmd.Flags().StringVar(&action, "action", "", "action prefix, e.g. release. or domain.add")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "how many events (max 500)")
	return cmd
}

func newServerCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "server", Short: "Show the servers environments run on"})
	cmd.AddCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List servers",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			servers, err := get[[]apitypes.Server](cmd.Context(), s, apiPrefix+"/servers")
			if err != nil {
				return err
			}
			return g.io.Print(servers, func() error {
				t := g.io.Table("name", "role", "builder", "status", "environments")
				for _, sv := range servers {
					builder := "no"
					if sv.Builder {
						builder = "yes"
					}
					t.Row(sv.Name, sv.Role, builder, sv.Status, strconv.Itoa(sv.Environments))
				}
				return t.Flush()
			})
		},
	})
	return cmd
}
