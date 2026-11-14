package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Groups and grants (ADR 0080): `dock group`, `dock grant`, `dock can`, `dock role`.
// (`dock access` is a service's access protection, ADR 0030.)

// grantFlags are the flags that say where a grant applies and for how
// long.
type grantFlags struct {
	project, environment string
	kinds                []string
	forDur               string
}

func (f *grantFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.project, "project", "p", "", "on this project (default: the whole organization)")
	cmd.Flags().StringVarP(&f.environment, "environment", "e", "", "on this environment of --project")
	cmd.Flags().StringSliceVar(&f.kinds, "kinds", nil, "only these environment kinds: production, other, previews (default all)")
	cmd.Flags().StringVar(&f.forDur, "for", "", "end it by itself after this long, like 4h or 30d (default never)")
}

// request is the grant the flags describe, of role.
func (f grantFlags) request(role string) (apitypes.GrantRequest, error) {
	req := apitypes.GrantRequest{Role: role, Project: f.project, Environment: f.environment, Kinds: f.kinds}
	if f.forDur == "" {
		return req, nil
	}
	d, err := parseLifetime(f.forDur)
	if err != nil || d < time.Hour {
		return req, usageError(fmt.Sprintf("--for %q isn't a lifetime of an hour or more", f.forDur), "Use hours or days, like --for 4h or --for 30d.")
	}
	req.ForHours = int(d / time.Hour)
	return req, nil
}

// parseLifetime reads 4h, 90m or 30d.
func parseLifetime(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n int
		if _, err := fmt.Sscan(days, &n); err != nil || n <= 0 {
			return 0, fmt.Errorf("bad days %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// grantPlace is a grant's place in words: "the org", "shop", "shop/staging (other)".
func grantPlace(g apitypes.Grant) string {
	place := "the org"
	switch {
	case g.Environment != "":
		place = g.Project + "/" + g.Environment
	case g.Project != "":
		place = g.Project
	}
	if len(g.Kinds) > 0 {
		place += " (" + strings.Join(g.Kinds, ", ") + ")"
	}
	if g.ExpiresAt != nil {
		place += ", until " + g.ExpiresAt.Local().Format("2006-01-02 15:04")
	}
	return place
}

func newGroupCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "group",
		Short: "Groups: job functions that give their members roles at places (admin)",
		Long: `A group gives its members built-in roles at places: the organization, a
project or one environment, optionally for some environment kinds
(` + "`dock role list`" + ` shows the roles). People are in groups; a group's
grants are theirs. Every organization starts with Admins, Developers,
Viewers and Approvers; a member's role (` + "`dock org role`" + `) is their place
in the first three. The organization always keeps someone with Full
access.`,
	})
	path := func(name string) string { return apiPrefix + "/groups/" + url.PathEscape(name) }
	printGroup := func(gr apitypes.Group) error {
		return g.io.Print(gr, func() error {
			g.io.Successf("%s", gr.Name)
			t := g.io.Table("grant", "role", "on")
			for _, x := range gr.Grants {
				t.Row(itoa(x.ID), x.Role, grantPlace(x))
			}
			if err := t.Flush(); err != nil {
				return err
			}
			var who []string
			for _, m := range gr.Members {
				who = append(who, m.Email)
			}
			g.io.Detail("Members: " + dash(strings.Join(who, ", ")))
			return nil
		})
	}
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List the groups with what they give and who is in them", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			groups, err := get[[]apitypes.Group](cmd.Context(), s, apiPrefix+"/groups")
			if err != nil {
				return err
			}
			return g.io.Print(groups, func() error {
				t := g.io.Table("group", "members", "gives")
				for _, gr := range groups {
					var gives []string
					for _, x := range gr.Grants {
						gives = append(gives, x.Role+" on "+grantPlace(x))
					}
					t.Row(gr.Name, itoa(int64(len(gr.Members))), dash(strings.Join(gives, "; ")))
				}
				return t.Flush()
			})
		},
	}
	show := &cobra.Command{
		Use: "show <group>", Short: "One group's grants and members", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			groups, err := get[[]apitypes.Group](cmd.Context(), s, apiPrefix+"/groups")
			if err != nil {
				return err
			}
			for _, gr := range groups {
				if gr.Name == args[0] || itoa(gr.ID) == args[0] {
					return printGroup(gr)
				}
			}
			return usageError(fmt.Sprintf("no group %q", args[0]), "`dock group list` lists them.")
		},
	}
	create := &cobra.Command{
		Use: "create <name>", Short: "Create an empty group", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			gr, err := send[apitypes.Group](cmd.Context(), s, http.MethodPost, apiPrefix+"/groups", apitypes.CreateGroupRequest{Name: args[0]})
			if err != nil {
				return err
			}
			return printGroup(gr)
		},
	}
	rename := &cobra.Command{
		Use: "rename <group> <new-name>", Short: "Rename a group", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			gr, err := send[apitypes.Group](cmd.Context(), s, http.MethodPatch, path(args[0]), apitypes.CreateGroupRequest{Name: args[1]})
			if err != nil {
				return err
			}
			return printGroup(gr)
		},
	}
	remove := &cobra.Command{
		Use: "delete <group>", Short: "Remove a group: its members lose what it gave them", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, path(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]any{"group": args[0], "deleted": true}, func() error {
				g.io.Successf("Removed group %s", args[0])
				return nil
			})
		},
	}
	add := &cobra.Command{
		Use: "add <group> <email|id>", Short: "Put a member in a group", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			gr, err := send[apitypes.Group](cmd.Context(), s, http.MethodPut, path(args[0])+"/members/"+url.PathEscape(args[1]), nil)
			if err != nil {
				return err
			}
			return printGroup(gr)
		},
	}
	take := &cobra.Command{
		Use: "remove <group> <email|id>", Short: "Take a member out of a group", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			gr, err := send[apitypes.Group](cmd.Context(), s, http.MethodDelete, path(args[0])+"/members/"+url.PathEscape(args[1]), nil)
			if err != nil {
				return err
			}
			return printGroup(gr)
		},
	}
	var gf grantFlags
	grant := &cobra.Command{
		Use: "grant <group> <role>", Short: "Give a group a role at a place", Args: cobra.ExactArgs(2),
		Example: `  dock group grant Contractors deploy:edit -p shop -e staging
  dock group grant Developers data:read --kinds other,previews
  dock group grant Marketing analytics:edit`,
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := gf.request(args[1])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			gr, err := send[apitypes.Group](cmd.Context(), s, http.MethodPost, path(args[0])+"/grants", req)
			if err != nil {
				return err
			}
			return printGroup(gr)
		},
	}
	gf.add(grant)
	cmd.AddCommand(list, show, create, rename, remove, add, take, grant, newRevokeGrantCmd(g))
	return cmd
}

// newRevokeGrantCmd ends a grant by its ID (a group's or a one-off).
func newRevokeGrantCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use: "revoke <grant-id>", Short: "End a grant (a group's or a one-off), by the ID `dock group show` and `dock grant list` show", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/grants/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			return g.io.Print(map[string]any{"grant": args[0], "revoked": true}, func() error {
				g.io.Successf("Ended grant %s", args[0])
				return nil
			})
		},
	}
}

func newCanCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "can [email|id]",
		Short: "What you (or a member, admin) can do: each area role's level per environment, from groups and grants",
		Long: `Your effective access in the organization, worked out from your groups and
one-off grants: each area role's level on the organization and in each
environment you see. With a member's email or ID, theirs (admin).`,
		Example: "  dock can\n  dock can ola@example.com --json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			p := apiPrefix + "/access"
			if len(args) == 1 {
				p += "/" + url.PathEscape(args[0])
			}
			acc, err := get[apitypes.EffectiveAccess](cmd.Context(), s, p)
			if err != nil {
				return err
			}
			return g.io.Print(acc, func() error {
				g.io.Successf("%s, in %s", acc.User, dash(strings.Join(acc.Groups, ", ")))
				levels := func(rs []apitypes.RoleAt) string {
					var out []string
					for _, r := range rs {
						out = append(out, r.Role+" "+r.Level)
					}
					return dash(strings.Join(out, ", "))
				}
				if err := g.io.Fields("organization", levels(acc.Org)); err != nil {
					return err
				}
				t := g.io.Table("environment", "kind", "can")
				for _, e := range acc.Environments {
					t.Row(e.Project+"/"+e.Environment, e.Kind, levels(e.Roles))
				}
				return t.Flush()
			})
		},
	}
}

func newGrantCmd(g *globals) *cobra.Command {
	var gf grantFlags
	cmd := &cobra.Command{
		Use:     "grant <email|id> <role>",
		Short:   "Give one member a role at a place, not through a group (admin)",
		Example: "  dock grant ola@example.com data:full -p shop -e staging --for 4h",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := gf.request(args[1])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			gr, err := send[apitypes.Grant](cmd.Context(), s, http.MethodPost, apiPrefix+"/members/"+url.PathEscape(args[0])+"/grants", req)
			if err != nil {
				return err
			}
			return g.io.Print(gr, func() error {
				g.io.Successf("Gave %s %s on %s (grant %d)", gr.User, gr.Role, grantPlace(gr), gr.ID)
				return nil
			})
		},
	}
	gf.add(cmd)
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List one-off grants (admin)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			grants, err := get[[]apitypes.Grant](cmd.Context(), s, apiPrefix+"/grants")
			if err != nil {
				return err
			}
			return g.io.Print(grants, func() error {
				t := g.io.Table("grant", "member", "role", "on")
				for _, x := range grants {
					t.Row(itoa(x.ID), x.User, x.Role, grantPlace(x))
				}
				return t.Flush()
			})
		},
	}
	cmd.AddCommand(list, newRevokeGrantCmd(g))
	return cmd
}

func newRoleCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "role", Short: "The built-in roles groups and grants give"})
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "Every area role, its levels and their permissions", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			roles, err := get[[]apitypes.AreaRole](cmd.Context(), s, apiPrefix+"/roles")
			if err != nil {
				return err
			}
			return g.io.Print(roles, func() error {
				t := g.io.Table("role", "name", "permissions")
				for _, r := range roles {
					for _, l := range r.Levels {
						t.Row(l.Role, r.Name, strings.Join(l.Permissions, " "))
					}
				}
				return t.Flush()
			})
		},
	})
	return cmd
}
