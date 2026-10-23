package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newGitHubCmd is `dock github`: the install's GitHub App (ADR 0031),
// through which services deploy on every push with no webhook or token of
// their own.
func newGitHubCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "github", Short: "The GitHub App that deploys pushes without per-service webhooks or tokens"})
	app := group(&cobra.Command{Use: "app", Short: "Create or remove Dock's GitHub App (operator)"})
	app.AddCommand(newGitHubAppCreateCmd(g), newGitHubAppRemoveCmd(g))
	cmd.AddCommand(newGitHubStatusCmd(g), app, newGitHubInstallCmd(g), newGitHubAssignCmd(g), newGitHubUnassignCmd(g), newGitHubReposCmd(g))
	return cmd
}

func newGitHubStatusCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the GitHub App, its webhook and its installations (operator)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := get[apitypes.GitHubStatus](cmd.Context(), s, apiPrefix+"/github")
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				if st.HooksHint != "" {
					g.io.Warnf("%s", st.HooksHint)
				}
				if st.App == nil {
					g.io.Successf("Dock has no GitHub App. Create one with `dock github app create`.")
					return nil
				}
				if err := g.io.Fields("app", st.App.Name+" ("+st.App.URL+")", "owner", st.App.Owner, "webhook", dash(st.WebhookURL),
					"created", ago(st.App.CreatedAt)+" by "+st.App.CreatedBy); err != nil {
					return err
				}
				if st.InstallationsError != "" {
					g.io.Warnf("GitHub couldn't list the installations: %s", st.InstallationsError)
				}
				if len(st.Installations) == 0 {
					_, _ = fmt.Fprintln(g.io.Out, "\nNot installed anywhere yet. Install it for an org with `dock github install <org>`.")
					return nil
				}
				_, _ = fmt.Fprintln(g.io.Out)
				t := g.io.Table("id", "account", "repos", "services", "org")
				for _, i := range st.Installations {
					org := i.Org
					switch {
					case i.Missing:
						org += " (uninstalled on GitHub)"
					case org == "":
						org = "unassigned: `dock github assign " + strconv.FormatInt(i.ID, 10) + " <org>`"
					}
					if i.Suspended {
						org += " (suspended)"
					}
					t.Row(strconv.FormatInt(i.ID, 10), i.Account, dash(i.Repositories), strconv.Itoa(i.Connections), org)
				}
				return t.Flush()
			})
		},
	}
}

func newGitHubAppCreateCmd(g *globals) *cobra.Command {
	var req apitypes.GitHubAppRequest
	var open bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create Dock's GitHub App on GitHub (operator)",
		Long: `Create Dock's GitHub App. Prints a link (valid for an hour) that opens a
page on Dock, which sends the App's manifest to GitHub. Confirm the App's
name there; GitHub then sends you back to Dock, which stores the App's key.

The App can read code and metadata, post commit statuses, and receives
pushes at Dock's public address (` + "`dock address set`" + `). It is private:
only the account that owns it (yours, or --owner) can install it. Then
install it for an org with ` + "`dock github install <org>`" + `.`,
		Example: `  dock github app create
  dock github app create --owner acme`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			link, err := send[apitypes.GitHubLink](cmd.Context(), s, http.MethodPost, apiPrefix+"/github/app", req)
			if err != nil {
				return err
			}
			if strings.HasPrefix(link.URL, "/") {
				link.URL = strings.TrimSuffix(s.server.Server, "/") + link.URL
			}
			return g.io.Print(link, func() error {
				g.openLink(link.URL, open, "to create the GitHub App")
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&req.Owner, "owner", "", "GitHub organization to own the App (default: your own account)")
	cmd.Flags().BoolVar(&open, "open", true, "open the link in a browser")
	return cmd
}

func newGitHubAppRemoveCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Forget the GitHub App and its installations (operator)",
		Long: `Forget the GitHub App's key and installations. Services connected
through it can't fetch until they are reconnected. Delete the App on
GitHub too.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm("Remove Dock's GitHub App? Services connected through it stop deploying.", "yes"); err != nil {
					return err
				}
			}
			if err := del(cmd.Context(), s, apiPrefix+"/github/app"); err != nil {
				return err
			}
			return g.io.Print(struct {
				Removed bool `json:"removed"`
			}{true}, func() error {
				g.io.Successf("Removed the GitHub App; delete it on GitHub too")
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func newGitHubInstallCmd(g *globals) *cobra.Command {
	var open bool
	cmd := &cobra.Command{
		Use:   "install <org>",
		Short: "Install the GitHub App for an org (operator)",
		Long: `Print the GitHub App's install link for an org (valid for an hour). On
GitHub, choose the account and repositories; GitHub then sends you back to
Dock, which lets the org deploy from them.`,
		Example: "  dock github install default",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			link, err := send[apitypes.GitHubLink](cmd.Context(), s, http.MethodPost, apiPrefix+"/github/installations", apitypes.GitHubOrgRequest{Org: args[0]})
			if err != nil {
				return err
			}
			return g.io.Print(link, func() error {
				g.openLink(link.URL, open, "to install the GitHub App for "+args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&open, "open", true, "open the link in a browser")
	return cmd
}

func newGitHubAssignCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "assign <installation-id> <org>",
		Short: "Let an org deploy from an installation's repositories (operator)",
		Long: `Assign one of the GitHub App's installations (see ` + "`dock github status`" + `)
to an org, which may then connect its repositories. An installation
assigned to another org moves; that org's services stop fetching from it.`,
		Example: "  dock github assign 4242 default",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := installationID(args[0])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := s.api.JSON(cmd.Context(), http.MethodPut, apiPrefix+"/github/installations/"+id, apitypes.GitHubOrgRequest{Org: args[1]}, nil); err != nil {
				return err
			}
			return g.io.Print(struct {
				Assigned bool `json:"assigned"`
			}{true}, func() error {
				g.io.Successf("Org %s can deploy from installation %s", args[1], id)
				return nil
			})
		},
	}
}

func newGitHubUnassignCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "unassign <installation-id>",
		Short: "Stop an org deploying from an installation (operator)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := installationID(args[0])
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/github/installations/"+id); err != nil {
				return err
			}
			return g.io.Print(struct {
				Unassigned bool `json:"unassigned"`
			}{true}, func() error {
				g.io.Successf("Installation %s is unassigned", id)
				return nil
			})
		},
	}
}

func newGitHubReposCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "repos",
		Short: "List the repositories the org can connect through the GitHub App",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			repos, err := get[apitypes.GitHubRepos](cmd.Context(), s, apiPrefix+"/github/repos")
			if err != nil {
				return err
			}
			return g.io.Print(repos, func() error {
				if len(repos.Repos) == 0 {
					g.io.Successf("No repositories: the GitHub App isn't installed for this org; an operator runs `dock github install <org>`")
					return nil
				}
				t := g.io.Table("repository", "default branch", "visibility")
				for _, r := range repos.Repos {
					vis := "public"
					if r.Private {
						vis = "private"
					}
					t.Row(r.FullName, r.DefaultBranch, vis)
				}
				if err := t.Flush(); err != nil {
					return err
				}
				if repos.Truncated {
					g.io.Warnf("Only the first %d are listed", len(repos.Repos))
				}
				return nil
			})
		},
	}
}

// installationID checks an installation ID argument.
func installationID(arg string) (string, error) {
	if n, err := strconv.ParseInt(arg, 10, 64); err != nil || n <= 0 {
		return "", usageError(fmt.Sprintf("%q is not an installation ID", arg), "See the IDs with `dock github status`.")
	}
	return arg, nil
}

// openLink prints a link to continue in a browser (on stderr with --json,
// so stdout stays one JSON value) and opens it when it can.
func (g *globals) openLink(link string, open bool, what string) {
	if u, err := url.Parse(link); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		g.io.Warnf("the server sent an invalid link %q", link)
		return
	}
	_, _ = fmt.Fprintf(g.io.Err, "Open %s %s (valid for an hour)\n", link, what)
	if open && canOpenBrowser() && openBrowser(link) == nil {
		g.io.Progressf("Opened your browser")
	}
}
