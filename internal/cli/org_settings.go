package cli

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// The organization's settings (ADR 0027): members, invites, variables,
// its name, leaving and deleting it. The web UI's organization settings
// do the same.

func newOrgMembersCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "members",
		Short: "List the organization's members and their roles (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			users, err := get[[]apitypes.User](cmd.Context(), s, apiPrefix+"/org/members")
			if err != nil {
				return err
			}
			return g.io.Print(users, func() error {
				t := g.io.Table("id", "email", "name", "role")
				for _, u := range users {
					t.Row(itoa(u.ID), u.Email, u.Name, string(u.Role))
				}
				return t.Flush()
			})
		},
	}
}

// findMember resolves a member of the organization by email or id.
func findMember(cmd *cobra.Command, s *session, who string) (apitypes.User, error) {
	users, err := get[[]apitypes.User](cmd.Context(), s, apiPrefix+"/org/members")
	if err != nil {
		return apitypes.User{}, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, who) || itoa(u.ID) == who {
			return u, nil
		}
	}
	return apitypes.User{}, &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("no member %q", who), Hint: "Members are shown by `dock org members`."}
}

func newOrgMemberCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "member", Short: "Change a member's role or remove them (admin)"})
	role := &cobra.Command{
		Use:   "role <email|id> <read|deploy|admin>",
		Short: "Change a member's role; the organization always keeps an admin",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			u, err := findMember(cmd, s, args[0])
			if err != nil {
				return err
			}
			u, err = send[apitypes.User](cmd.Context(), s, http.MethodPatch, apiPrefix+"/org/members/"+itoa(u.ID), apitypes.SetMemberRoleRequest{Role: apitypes.Scope(args[1])})
			if err != nil {
				return err
			}
			return g.io.Print(u, func() error {
				g.io.Successf("%s is %s now", u.Email, u.Role)
				return nil
			})
		},
	}
	var yes bool
	remove := &cobra.Command{
		Use:   "remove <email|id>",
		Short: "Take a member out of the organization; their account stays",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			u, err := findMember(cmd, s, args[0])
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Remove %s from the organization?", u.Email), "yes"); err != nil {
					return err
				}
			}
			if err := del(cmd.Context(), s, apiPrefix+"/org/members/"+itoa(u.ID)); err != nil {
				return err
			}
			v := struct {
				Email   string `json:"email"`
				Removed bool   `json:"removed"`
			}{u.Email, true}
			return g.io.Print(v, func() error {
				g.io.Successf("Removed %s", u.Email)
				return nil
			})
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.AddCommand(role, remove)
	return cmd
}

// inviteView is an invite with its link, as `dock org invite` prints it.
type inviteView struct {
	apitypes.CreatedInvite
	URL string `json:"url"`
}

func newOrgInviteCmd(g *globals) *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:   "invite <email>",
		Short: "Invite someone; prints the link to send them (admin)",
		Long: `Invite an email address with a role. The link works once, for 7 days, and
only for someone signed in with that email; they open it in the browser or
run ` + "`dock org join <link>`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			created, err := send[apitypes.CreatedInvite](cmd.Context(), s, http.MethodPost, apiPrefix+"/org/invites", apitypes.CreateInviteRequest{Email: args[0], Role: apitypes.Scope(role)})
			if err != nil {
				return err
			}
			link := cmp.Or(created.URL, strings.TrimRight(s.server.Server, "/")+"/invite/"+created.Code)
			v := inviteView{created, link}
			return g.io.Print(v, func() error {
				g.io.Successf("Invited %s as %s", created.Invite.Email, created.Invite.Role)
				_, err := fmt.Fprintln(g.io.Out, v.URL)
				g.io.Detail("Send them the link; it works once, for 7 days. It isn't shown again.")
				return err
			})
		},
	}
	cmd.Flags().StringVar(&role, "role", "deploy", "read, deploy or admin")
	return cmd
}

func newOrgInvitesCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invites",
		Short: "List open invites (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			invites, err := get[[]apitypes.Invite](cmd.Context(), s, apiPrefix+"/org/invites")
			if err != nil {
				return err
			}
			return g.io.Print(invites, func() error {
				t := g.io.Table("id", "email", "role", "invited by", "expires")
				for _, i := range invites {
					expires := i.ExpiresAt.Local().Format("2006-01-02 15:04")
					if i.Expired {
						expires = "expired"
					}
					t.Row(itoa(i.ID), i.Email, string(i.Role), i.CreatedBy, expires)
				}
				return t.Flush()
			})
		},
	}
	revoke := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Stop an invite's link working",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/org/invites/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			v := struct {
				ID      string `json:"id"`
				Revoked bool   `json:"revoked"`
			}{args[0], true}
			return g.io.Print(v, func() error {
				g.io.Successf("Revoked invite %s", args[0])
				return nil
			})
		},
	}
	cmd.AddCommand(revoke)
	return cmd
}

func newOrgJoinCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "join <link|code>",
		Short: "Accept an invite to an organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code := args[0]
			if i := strings.LastIndex(code, "/invite/"); i >= 0 {
				code = code[i+len("/invite/"):]
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			s.api.Org = "" // the invite names the org
			o, err := send[apitypes.Org](cmd.Context(), s, http.MethodPost, apiPrefix+"/invites/"+url.PathEscape(code)+"/accept", nil)
			if err != nil {
				return err
			}
			return g.io.Print(o, func() error {
				g.io.Successf("Joined %s as %s", o.Name, o.Role)
				g.io.Detail(fmt.Sprintf("Act in it with `dock org use %s`, or --org %s for one command.", o.Slug, o.Slug))
				return nil
			})
		},
	}
}

func newOrgEnvCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "env",
		Short: "Organization variables, referenced as ${{org.KEY}}",
		Long: `Variables every project of the organization may use, where a variable's
value says ${{org.KEY}}. They are never added to services by themselves.
A change reaches a service on its next release; ` + "`dock org env redeploy KEY`" + `
starts those.`,
	})
	var reveal bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List organization variables and where each is used",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/org/variables"
			if reveal {
				path += "?reveal=true"
			}
			vars, err := get[[]apitypes.OrgVariable](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(vars, func() error {
				t := g.io.Table("name", "value", "used by", "updated")
				for _, v := range vars {
					value := "(hidden)"
					if v.Value != nil {
						value = *v.Value
					}
					t.Row(v.Key, value, dash(describeVarUses(v.UsedBy)), ago(v.UpdatedAt))
				}
				return t.Flush()
			})
		},
	}
	list.Flags().BoolVar(&reveal, "reveal", false, "show values (admin)")
	set := &cobra.Command{
		Use:   "set KEY=value...",
		Short: "Set organization variables (admin)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			var out []apitypes.OrgVariable
			for _, a := range args {
				k, val, ok := strings.Cut(a, "=")
				if !ok || k == "" {
					return usageError(fmt.Sprintf("%q is not KEY=value", a), "Quote values with spaces: `dock org env set 'GREETING=hello world'`.")
				}
				v, err := send[apitypes.OrgVariable](cmd.Context(), s, http.MethodPut, apiPrefix+"/org/variables/"+url.PathEscape(k), apitypes.SetOrgVariableRequest{Value: val})
				if err != nil {
					return err
				}
				out = append(out, v)
			}
			return g.io.Print(out, func() error {
				for _, v := range out {
					g.io.Successf("Set %s", v.Key)
					if len(v.UsedBy) > 0 {
						g.io.Detail(fmt.Sprintf("Used by %s; they get it on their next release, or now with `dock org env redeploy %s`.", describeVarUses(v.UsedBy), v.Key))
					}
				}
				return nil
			})
		},
	}
	unset := &cobra.Command{
		Use:   "unset KEY...",
		Short: "Remove organization variables nothing uses (admin)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			for _, k := range args {
				if err := del(cmd.Context(), s, apiPrefix+"/org/variables/"+url.PathEscape(k)); err != nil {
					return err
				}
			}
			v := struct {
				Removed []string `json:"removed"`
			}{args}
			return g.io.Print(v, func() error {
				g.io.Successf("Removed %s", strings.Join(args, ", "))
				return nil
			})
		},
	}
	redeploy := &cobra.Command{
		Use:   "redeploy KEY",
		Short: "Redeploy every running service that uses an organization variable",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			out, err := send[apitypes.OrgVariableRollout](cmd.Context(), s, http.MethodPost, apiPrefix+"/org/variables/"+url.PathEscape(args[0])+"/redeploy", nil)
			if err != nil {
				return err
			}
			return g.io.Print(out, func() error {
				for _, r := range out.Started {
					g.io.Successf("%s/%s/%s: release %d", r.Project, r.Environment, r.Service, r.Number)
				}
				for _, r := range out.Skipped {
					g.io.Warnf("%s/%s/%s: %s", r.Project, r.Environment, r.Service, r.Reason)
				}
				if len(out.Started)+len(out.Skipped) == 0 {
					g.io.Detail("Nothing uses " + args[0] + ".")
				}
				return nil
			})
		},
	}
	cmd.AddCommand(list, set, unset, redeploy)
	return cmd
}

// describeVarUses is "shop/production/web, blog/staging (shared)".
func describeVarUses(uses []apitypes.VariableUse) string {
	parts := make([]string, len(uses))
	for i, u := range uses {
		parts[i] = u.Project + "/" + u.Environment
		if u.Service != "" {
			parts[i] += "/" + u.Service
		} else {
			parts[i] += " (shared)"
		}
	}
	return strings.Join(parts, ", ")
}

// newOrgRenameCmd is `dock org rename [<name>] [--slug <slug>]`. A new
// slug is also written into this machine's contexts and links on the same
// server that named the old one; the old slug keeps working anyway.
func newOrgRenameCmd(g *globals) *cobra.Command {
	var slug string
	cmd := &cobra.Command{
		Use:   "rename [<name>] [--slug <slug>]",
		Short: "Change the organization's name, its slug (its address), or both (admin)",
		Long: `Change the organization's display name, its slug, or both.

The slug is its address: /<slug>/... in the browser and --org <slug> here.
After a change the old slug keeps working for links, scripts and other
people's CLI contexts, until another organization takes it.`,
		Example: "  dock org rename \"NordTempo\"\n  dock org rename --slug nordtempo\n  dock org rename \"NordTempo\" --slug nordtempo",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := apitypes.UpdateOrgRequest{Slug: slug}
			if len(args) == 1 {
				req.Name = args[0]
			}
			if req.Name == "" && req.Slug == "" {
				return &output.Error{Exit: output.ExitUsage, Message: "give a new name, --slug, or both", Hint: "For example `dock org rename --slug nordtempo`."}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			var before apitypes.Org
			if req.Slug != "" {
				if before, err = get[apitypes.Org](cmd.Context(), s, apiPrefix+"/org"); err != nil {
					return err
				}
			}
			o, err := send[apitypes.Org](cmd.Context(), s, http.MethodPatch, apiPrefix+"/org", req)
			if err != nil {
				return err
			}
			moved := before.Slug != "" && before.Slug != o.Slug
			if moved && s.cfg.followSlug(s.server.Server, before.Slug, o.Slug) {
				if err := s.cfg.save(); err != nil {
					return err
				}
			}
			return g.io.Print(o, func() error {
				if req.Name != "" {
					g.io.Successf("Renamed %s to %s", o.Slug, o.Name)
				}
				if moved {
					g.io.Successf("Moved %s to %s; %s keeps working until another organization takes it", before.Slug, o.Slug, before.Slug)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&slug, "slug", "", "new slug: lowercase letters, digits and dashes, at most 32")
	return cmd
}

// followSlug points the contexts and links for server that name org
// slug from at to instead, and reports whether any did.
func (c userConfig) followSlug(server, from, to string) bool {
	changed := false
	for name, ctx := range c.Contexts {
		if ctx.Server != server {
			continue
		}
		if ctx.Org == from {
			ctx.Org, changed = to, true
			c.Contexts[name] = ctx
		}
		for dir, l := range c.Links {
			if l.Context == name && l.Org == from {
				l.Org, changed = to, true
				c.Links[dir] = l
			}
		}
	}
	return changed
}

// newOrgGoneCmd is `dock org leave` and `dock org delete`: confirmed, then
// method on /org (path suffix).
func newOrgGoneCmd(g *globals, use, short, method, suffix, question, done string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			o, err := get[apitypes.Org](cmd.Context(), s, apiPrefix+"/org")
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf(question, o.Name), "yes"); err != nil {
					return err
				}
			}
			if err := s.api.JSON(cmd.Context(), method, apiPrefix+"/org"+suffix, nil, nil); err != nil {
				return err
			}
			v := struct {
				Org  string `json:"org"`
				Done bool   `json:"done"`
			}{o.Slug, true}
			return g.io.Print(v, func() error {
				g.io.Successf(done, o.Name)
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
