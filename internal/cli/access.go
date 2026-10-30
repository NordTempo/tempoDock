package cli

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Admin commands: tokens, users, the audit log and servers.

func newTokenCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "token",
		Short: "Manage API tokens (admin; your own with any role)",
		Long: `API tokens for the CLI, CI and agents. A token has a scope (read < deploy <
admin) and can be limited to one project or one environment of it. Its
database access follows the data policy's tokens rule unless it has its own
(--data-* on create or update).`,
	})
	cmd.AddCommand(newTokenListCmd(g), newTokenCreateCmd(g), newTokenUpdateCmd(g), newTokenRevokeCmd(g))
	return cmd
}

func newTokenListCmd(g *globals) *cobra.Command {
	var mine bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List tokens",
		Long: `List the organization's tokens and its members' ` + "`dock login`" + ` tokens
(admin). --mine lists only yours, with any role: your ` + "`dock login`" + ` tokens
and the tokens you created. You can revoke your own with ` + "`dock token revoke`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/tokens"
			if mine {
				path = apiPrefix + "/me/tokens"
			}
			tokens, err := get[[]apitypes.Token](cmd.Context(), s, path)
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
	cmd.Flags().BoolVar(&mine, "mine", false, "only your own tokens")
	return cmd
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
	var operator bool
	var data dataFlags
	cmd := &cobra.Command{
		Use:   "create <name> --scope read|deploy|admin",
		Short: "Create a token; its secret is shown once",
		Long: `Create a token. The secret is printed once (also with --json) and can't be
shown again. The token belongs to the organization in use. --project and
--environment limit the token; they are never taken from the directory's
link. --operator (operators only) makes an admin token that may also run
the platform: servers, backups, accounts.`,
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
			req := apitypes.CreateTokenRequest{Name: args[0], Scope: apitypes.Scope(scope), Project: project, Environment: env, ExpiresInDays: days, Operator: operator}
			if req.Data, err = data.access(cmd, nil); err != nil {
				return err
			}
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
				if ct.Token.Data != nil {
					g.io.Progressf("Database access: %s", dataAccessText(ct.Token.Data))
				}
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
	cmd.Flags().BoolVar(&operator, "operator", false, "a platform operator token (operators only; needs --scope admin)")
	data.add(cmd)
	return cmd
}

// dataFlags are a token's own database access: --data-production,
// --data-non-production and --data-sensitive.
type dataFlags struct {
	production, nonProduction, sensitive string
}

func (f *dataFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.production, "data-production", "", "database access on production: none, schema, read, propose (write acts as propose for tokens)")
	cmd.Flags().StringVar(&f.nonProduction, "data-non-production", "", "database access elsewhere: none, schema, read, propose or write")
	cmd.Flags().StringVar(&f.sensitive, "data-sensitive", "", "sensitive columns: masked or visible")
}

// access returns the token's data access with the flags given applied to
// base (nil: none, none, masked), or nil when no flag was given.
func (f *dataFlags) access(cmd *cobra.Command, base *apitypes.DataTokenAccess) (*apitypes.DataTokenAccess, error) {
	if !cmd.Flags().Changed("data-production") && !cmd.Flags().Changed("data-non-production") && !cmd.Flags().Changed("data-sensitive") {
		return nil, nil
	}
	a := apitypes.DataTokenAccess{Production: apitypes.LevelNone, NonProduction: apitypes.LevelNone, Sensitive: apitypes.SensitiveMasked}
	if base != nil {
		a = *base
	}
	for _, l := range []struct {
		flag, value string
		to          *apitypes.DataLevel
	}{{"data-production", f.production, &a.Production}, {"data-non-production", f.nonProduction, &a.NonProduction}} {
		if !cmd.Flags().Changed(l.flag) {
			continue
		}
		if !slices.Contains(dataLevels, apitypes.DataLevel(l.value)) {
			return nil, usageError(fmt.Sprintf("invalid --%s %q", l.flag, l.value), "Use none, schema, read, propose or write.")
		}
		*l.to = apitypes.DataLevel(l.value)
	}
	if cmd.Flags().Changed("data-sensitive") {
		a.Sensitive = apitypes.DataSensitivity(f.sensitive)
		if a.Sensitive != apitypes.SensitiveMasked && a.Sensitive != apitypes.SensitiveVisible {
			return nil, usageError(fmt.Sprintf("invalid --data-sensitive %q", f.sensitive), "Use masked or visible.")
		}
	}
	return &a, nil
}

// dataAccessText says what a token's data access is.
func dataAccessText(a *apitypes.DataTokenAccess) string {
	if a == nil {
		return "follows the data policy"
	}
	return fmt.Sprintf("production %s, non-production %s, sensitive %s", a.Production, a.NonProduction, a.Sensitive)
}

func newTokenUpdateCmd(g *globals) *cobra.Command {
	var data dataFlags
	var clear bool
	cmd := &cobra.Command{
		Use:   "update <id> [--data-production L] [--data-non-production L] [--data-sensitive S] | --data-clear",
		Short: "Change a token's own database access",
		Long: `Set a token's own database access, overriding the data policy's tokens rule
(flags not given keep their value), or remove it with --data-clear so the
token follows the policy again. Either way a token never gets more than
users with its scope, and never writes production directly.`,
		Example: `  dock token update 7 --data-non-production read
  dock token update 7 --data-clear`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return usageError(fmt.Sprintf("%q is not a token id", args[0]), "Token ids are shown by `dock token list`.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			var req apitypes.UpdateTokenRequest
			if !clear {
				tokens, err := get[[]apitypes.Token](cmd.Context(), s, apiPrefix+"/tokens")
				if err != nil {
					return err
				}
				i := slices.IndexFunc(tokens, func(t apitypes.Token) bool { return t.ID == id })
				if i < 0 {
					return &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("no token %d", id), Hint: "Token ids are shown by `dock token list`."}
				}
				if req.Data, err = data.access(cmd, tokens[i].Data); err != nil {
					return err
				}
				if req.Data == nil {
					return usageError("nothing to change", "Pass --data-production, --data-non-production, --data-sensitive or --data-clear.")
				}
			}
			tk, err := send[apitypes.Token](cmd.Context(), s, http.MethodPatch, apiPrefix+"/tokens/"+itoa(id), req)
			if err != nil {
				return err
			}
			return g.io.Print(tk, func() error {
				g.io.Successf("Token %s (id %d): database access %s", tk.Name, tk.ID, dataAccessText(tk.Data))
				return nil
			})
		},
	}
	data.add(cmd)
	cmd.Flags().BoolVar(&clear, "data-clear", false, "remove the token's own access; it follows the data policy")
	cmd.MarkFlagsMutuallyExclusive("data-clear", "data-production")
	cmd.MarkFlagsMutuallyExclusive("data-clear", "data-non-production")
	cmd.MarkFlagsMutuallyExclusive("data-clear", "data-sensitive")
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
				t := g.io.Table("id", "email", "name", "role", "status", "sessions", "tokens", "created")
				for _, u := range users {
					status := "active"
					if u.DisabledAt != nil {
						status = "disabled"
					}
					t.Row(itoa(u.ID), u.Email, u.Name, string(u.Role), status, strconv.Itoa(u.Sessions), strconv.Itoa(u.Tokens), ago(u.CreatedAt))
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
				if u.Operator {
					g.io.Successf("%s is a platform operator", u.Email)
				}
				return nil
			})
		},
	}
	create.Flags().StringVar(&req.Email, "email", "", "login email")
	create.Flags().StringVar(&req.Name, "name", "", "display name")
	create.Flags().StringVar(&role, "role", "", "read, deploy or admin")
	create.Flags().BoolVar(&req.Operator, "operator", false, "make them a platform operator too")
	create.Flags().StringVar(&req.Password, "password", "", "password (prompted for on a terminal)")

	cmd.AddCommand(list, create,
		newUserStateCmd(g, "disable", "Disable a user: they can't log in, their sessions and dock login tokens end", "disable", "Disabled"),
		newUserStateCmd(g, "enable", "Let a disabled user log in again", "enable", "Enabled"),
		newUserDeleteCmd(g), newUserResetPasswordCmd(g))
	return cmd
}

// findUser resolves a user given by email or id.
func findUser(cmd *cobra.Command, s *session, who string) (apitypes.User, error) {
	users, err := get[[]apitypes.User](cmd.Context(), s, apiPrefix+"/users")
	if err != nil {
		return apitypes.User{}, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, who) || itoa(u.ID) == who {
			return u, nil
		}
	}
	return apitypes.User{}, &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("no user %q", who), Hint: "Users are shown by `dock user list`."}
}

func newUserStateCmd(g *globals, use, short, action, done string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <email|id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			u, err := findUser(cmd, s, args[0])
			if err != nil {
				return err
			}
			u, err = send[apitypes.User](cmd.Context(), s, http.MethodPost, apiPrefix+"/users/"+itoa(u.ID)+"/"+action, nil)
			if err != nil {
				return err
			}
			return g.io.Print(u, func() error {
				g.io.Successf("%s user %s", done, u.Email)
				return nil
			})
		},
	}
}

func newUserDeleteCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <email|id>",
		Short: "Delete a user; their dock login tokens end, audit history and other tokens stay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			u, err := findUser(cmd, s, args[0])
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Delete user %s?", u.Email), "yes"); err != nil {
					return err
				}
			}
			if err := del(cmd.Context(), s, apiPrefix+"/users/"+itoa(u.ID)); err != nil {
				return err
			}
			v := struct {
				ID      int64  `json:"id"`
				Email   string `json:"email"`
				Deleted bool   `json:"deleted"`
			}{u.ID, u.Email, true}
			return g.io.Print(v, func() error {
				g.io.Successf("Deleted user %s", u.Email)
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func newUserResetPasswordCmd(g *globals) *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "reset-password <email|id> [--password <new>]",
		Short: "Set a user's password, or generate one; their sessions and dock login tokens end",
		Long: `Reset another user's password. Without --password a password is generated
and printed once. Change your own with ` + "`dock me password`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			u, err := findUser(cmd, s, args[0])
			if err != nil {
				return err
			}
			res, err := send[apitypes.ResetPasswordResult](cmd.Context(), s, http.MethodPost, apiPrefix+"/users/"+itoa(u.ID)+"/reset-password",
				apitypes.ResetPasswordRequest{Password: password})
			if err != nil {
				return err
			}
			return g.io.Print(res, func() error {
				g.io.Successf("Reset the password of %s; their sessions ended", res.User.Email)
				if res.Password != "" {
					_, _ = fmt.Fprintln(g.io.Out, res.Password)
					g.io.Warnf("Pass the password on now; it is never shown again.")
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "the new password (at least 12 characters); generated when omitted")
	return cmd
}

func newAuditCmd(g *globals) *cobra.Command {
	var project, env, service, action string
	var limit int
	var platform bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show the audit log, newest first",
		Long: `Every mutating action in the organization with who did it. Filters are
explicit flags; the directory's link is not used. --platform shows the
platform's log instead (servers, accounts, backup settings; operators).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			q := url.Values{"limit": {strconv.Itoa(limit)}}
			if platform {
				q.Set("platform", "true")
			}
			for k, v := range map[string]string{"project": project, "environment": env, "service": service, "action": action} {
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
	cmd.Flags().StringVarP(&service, "service", "s", "", "only events targeting this service of --project and --environment")
	cmd.Flags().StringVar(&action, "action", "", "action prefix, e.g. release. or domain.add")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "how many events (max 500)")
	cmd.Flags().BoolVar(&platform, "platform", false, "the platform's log instead of the organization's (operators)")
	return cmd
}
