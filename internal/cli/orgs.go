package cli

import (
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// orgView is the JSON shape of an organization in `dock org list`.
type orgView struct {
	apitypes.Org
	Current bool `json:"current"`
}

func newOrgCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "org",
		Short: "Organizations: choose one, members, invites, variables",
		Long: `Projects and tokens belong to an organization, and you act in one at a
time with your role there. With one organization you never need to name
it. With several, pick one for the context with ` + "`dock org use`" + `, for one
command with --org, or with DOCK_ORG; a linked directory acts in the
organization it was linked in.`,
	})
	cmd.AddCommand(newOrgListCmd(g), newOrgCreateCmd(g), newOrgUseCmd(g), newOrgLocksCmd(g),
		newOrgMembersCmd(g), newOrgMemberCmd(g), newOrgInviteCmd(g), newOrgInvitesCmd(g), newOrgJoinCmd(g),
		newOrgEnvCmd(g), newOrgRenameCmd(g), newOrgTwoFactorCmd(g), newOrgSSOMovedCmd("sso"), newOrgSSOMovedCmd("sso-only"),
		newOrgLeaveMovedCmd(),
		newOrgGoneCmd(g, "delete", "Delete an organization without projects (admin)", "DELETE", "",
			"Delete %s? Its members, invites, variables and tokens go.", "Deleted %s"))
	return cmd
}

func newOrgListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List your organizations",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			orgs, err := get[[]apitypes.Org](cmd.Context(), s, apiPrefix+"/orgs")
			if err != nil {
				return err
			}
			views := make([]orgView, len(orgs))
			for i, o := range orgs {
				views[i] = orgView{o, o.Slug == s.server.Org || s.server.Org == "" && len(orgs) == 1}
			}
			return g.io.Print(views, func() error {
				t := g.io.Table("", "slug", "name", "role", "two-factor")
				for _, v := range views {
					current := ""
					if v.Current {
						current = "*"
					}
					t.Row(current, v.Slug, v.Name, string(v.Role), requiredWord(v.RequireTwoFactor))
				}
				return t.Flush()
			})
		},
	}
}

func newOrgCreateCmd(g *globals) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "create <slug>",
		Short: "Create an organization, with you as its admin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			o, err := send[apitypes.Org](cmd.Context(), s, "POST", apiPrefix+"/orgs", apitypes.CreateOrgRequest{Slug: args[0], Name: name})
			if err != nil {
				return err
			}
			return g.io.Print(o, func() error {
				g.io.Successf("Created organization %s", o.Slug)
				g.io.Detail(fmt.Sprintf("Act in it with `dock org use %s`, or --org %s for one command.", o.Slug, o.Slug))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name (default: the slug)")
	return cmd
}

func newOrgUseCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "use <slug>",
		Short: "Act in an organization from now on, in the context in use",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			s, err := g.connect()
			if err != nil {
				return err
			}
			if s.server.Name == "" {
				return &output.Error{
					Exit:    output.ExitUsage,
					Message: fmt.Sprintf("%s is in use, which has no context to remember the organization in", clientconfig.EnvURL),
					Hint:    fmt.Sprintf("Set %s=%s instead.", clientconfig.EnvOrg, slug),
				}
			}
			s.api.Org = ""
			orgs, err := get[[]apitypes.Org](cmd.Context(), s, apiPrefix+"/orgs")
			if err != nil {
				return err
			}
			i := slices.IndexFunc(orgs, func(o apitypes.Org) bool { return o.Slug == slug })
			if i < 0 {
				// An old slug names the org it moved to (ADR 0064).
				s.api.Org = slug
				if o, err := get[apitypes.Org](cmd.Context(), s, apiPrefix+"/org"); err == nil {
					slug = o.Slug
					i = slices.IndexFunc(orgs, func(o apitypes.Org) bool { return o.Slug == slug })
				}
			}
			if i < 0 {
				return &output.Error{
					Exit:    output.ExitNotFound,
					Message: fmt.Sprintf("organization %q not found among yours", slug),
					Hint:    "Run `dock org list` to see them.",
				}
			}
			c := s.cfg.Contexts[s.server.Name]
			c.Org = slug
			s.cfg.Contexts[s.server.Name] = c
			if err := s.cfg.save(); err != nil {
				return err
			}
			v := orgView{orgs[i], true}
			return g.io.Print(v, func() error {
				g.io.Successf("Now acting in %s in context %s", slug, s.server.Name)
				return nil
			})
		},
	}
}
