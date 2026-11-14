package cli

import (
	"cmp"
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
	app.AddCommand(newGitHubAppCreateCmd(g), newGitHubAppSecretCmd(g), newGitHubAppRemoveCmd(g))
	cmd.AddCommand(newGitHubStatusCmd(g), app, newGitHubInstallationsCmd(g), newGitHubInstallCmd(g), newGitHubAssignCmd(g), newGitHubUnassignCmd(g), newGitHubReposCmd(g), newGitHubBranchesCmd(g), newGitHubPathsCmd(g))
	return cmd
}

// newGitHubInstallationsCmd is `dock github installations`: the App as the
// org's admins see it.
func newGitHubInstallationsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "installations",
		Short: "Show the GitHub App and the organization's installations (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := get[apitypes.GitHubOrg](cmd.Context(), s, apiPrefix+"/github/org")
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				if st.App == nil {
					g.io.Successf("The platform has no GitHub App yet; an operator creates one with `dock github app create`.")
					return nil
				}
				if err := g.io.Fields("app", st.App.Name+" ("+st.App.URL+")", "owner", st.App.Owner); err != nil {
					return err
				}
				if st.InstallationsError != "" {
					g.io.Warnf("GitHub couldn't list the installations: %s", st.InstallationsError)
				}
				if len(st.Installations) == 0 {
					_, _ = fmt.Fprintln(g.io.Out, "\nNot installed for this organization yet. Install it with `dock github install`.")
					return nil
				}
				_, _ = fmt.Fprintln(g.io.Out)
				t := g.io.Table("id", "account", "repos", "services", "state")
				for _, i := range st.Installations {
					state := "ok"
					switch {
					case i.Missing:
						state = "uninstalled on GitHub"
					case i.Suspended:
						state = "suspended"
					}
					t.Row(strconv.FormatInt(i.ID, 10), i.Account, dash(i.Repositories), strconv.Itoa(i.Connections), state)
				}
				return t.Flush()
			})
		},
	}
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
				installs := "checked against the installer's GitHub account"
				if !st.App.VerifiesInstalls {
					installs = "unchecked: only operators' install links work"
				}
				if err := g.io.Fields("app", st.App.Name+" ("+st.App.URL+")", "owner", st.App.Owner, "webhook", dash(st.WebhookURL),
					"installs", installs, "created", ago(st.App.CreatedAt)+" by "+st.App.CreatedBy); err != nil {
					return err
				}
				if !st.App.VerifiesInstalls {
					g.io.Warnf("The App was made before Dock kept its client secret. In its settings on GitHub, add the callback URL %s and generate a client secret, then: printf %%s \"$SECRET\" | dock github app secret", st.CallbackURL)
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
pushes at Dock's public address (` + "`dock address set`" + `). It is private
unless --public: only the account that owns it (yours, or --owner) can
install it. A public App can be installed by any GitHub account, so orgs
deploy from their own; Dock checks with GitHub that whoever installs it
can access the installation. GitHub can make a private App public later,
but not the reverse once others have installed it. Then install it for an
org with ` + "`dock github install <org>`" + `.`,
		Example: `  dock github app create
  dock github app create --owner acme --public`,
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
	cmd.Flags().BoolVar(&req.Public, "public", false, "let any GitHub account install the App")
	cmd.Flags().BoolVar(&open, "open", true, "open the link in a browser")
	return cmd
}

// newGitHubAppSecretCmd is `dock github app secret`: the client secret of
// an App made before Dock kept it (ADR 0066), so installs are checked.
func newGitHubAppSecretCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "secret",
		Short: "Give Dock the GitHub App's client secret, read from stdin (operator)",
		Long: `Give Dock the client secret of a GitHub App made before Dock kept it.
In the App's settings on GitHub, add the callback URL ` + "`dock github status`" + `
shows and generate a client secret, then pipe it in. Dock checks it with
GitHub. From then on, Dock checks with GitHub that whoever installs the App
can access the installation, and an org's admins can install it for their
org. Apps made since need nothing.`,
		Example: `  printf %s "$CLIENT_SECRET" | dock github app secret`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			secret, err := readClientSecret(g, "dock github app secret")
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := s.api.JSON(cmd.Context(), http.MethodPut, apiPrefix+"/github/app/client-secret", apitypes.GitHubClientSecretRequest{ClientSecret: secret}, nil); err != nil {
				return err
			}
			return g.io.Print(struct {
				VerifiesInstalls bool `json:"verifies_installs"`
			}{true}, func() error {
				g.io.Successf("Stored the GitHub App's client secret; installs are now checked with GitHub")
				return nil
			})
		},
	}
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
		Use:   "install [<org>]",
		Short: "Install the GitHub App for the organization (admin; another org: operator)",
		Long: `Print the GitHub App's install link for the organization (valid for an hour).
It is yours alone: open it in the browser you are signed in to Dock with.
On GitHub, choose the account and repositories, then authorize the App:
Dock checks with GitHub that you can access the installation and, once you
confirm the organization, lets it deploy from it, unless another
organization has it. An operator may name any org.`,
		Example: `  dock github install
  dock github install default`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var req apitypes.GitHubOrgRequest
			what := "to install the GitHub App"
			if len(args) == 1 {
				req.Org = args[0]
				what += " for " + args[0]
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			link, err := send[apitypes.GitHubLink](cmd.Context(), s, http.MethodPost, apiPrefix+"/github/installations", req)
			if err != nil {
				return err
			}
			return g.io.Print(link, func() error {
				g.openLink(link.URL, open, what)
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
		Short: "Stop the organization deploying from an installation (admin; another org's: operator)",
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
					g.io.Successf("No repositories: the GitHub App isn't installed for this org; its admins install it with `dock github install`")
					return nil
				}
				t := g.io.Table("repository", "default branch", "visibility", "last push", "used by")
				for _, r := range repos.Repos {
					vis := "public"
					if r.Private {
						vis = "private"
					}
					pushed := ""
					if r.PushedAt != nil {
						pushed = ago(*r.PushedAt)
					}
					t.Row(r.FullName, r.DefaultBranch, vis, pushed, strings.Join(r.UsedBy, ", "))
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

// newGitHubBranchesCmd is `dock github branches <repo>`.
func newGitHubBranchesCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "branches <owner/repo>",
		Short: "List a repository's branches, for `dock service create --repo` and `dock git connect`",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			b, err := get[apitypes.GitHubBranches](cmd.Context(), s, apiPrefix+"/github/repos/"+repoPath(args[0])+"/branches")
			if err != nil {
				return err
			}
			return g.io.Print(b, func() error {
				t := g.io.Table("branch", "")
				for _, name := range b.Branches {
					note := ""
					if name == b.DefaultBranch {
						note = "default"
					}
					t.Row(name, note)
				}
				if err := t.Flush(); err != nil {
					return err
				}
				if b.Truncated {
					g.io.Warnf("Only the first %d are listed", len(b.Branches))
				}
				return nil
			})
		},
	}
}

// newGitHubPathsCmd is `dock github paths <repo>`: the directories a
// service could build, the ones that look like an app first.
func newGitHubPathsCmd(g *globals) *cobra.Command {
	var branch string
	var all bool
	cmd := &cobra.Command{
		Use:   "paths <owner/repo>",
		Short: "List a repository's directories that look like an app (a Dockerfile, package.json, go.mod...)",
		Long: `List the directories of a repository a service could build: the ones with a
Dockerfile, package.json, go.mod and the like, with the services that already
build them. --all lists every other directory too. Dependency and hidden
directories are left out.`,
		Example: `  dock github paths acme/shop
  dock service create web --repo acme/shop --dir apps/web`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/github/repos/" + repoPath(args[0]) + "/paths"
			if branch != "" {
				path += "?branch=" + url.QueryEscape(branch)
			}
			p, err := get[apitypes.GitHubPaths](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(p, func() error {
				t := g.io.Table("directory", "files", "used by")
				for _, r := range p.Roots {
					t.Row(cmp.Or(r.Path, "(root)"), strings.Join(r.Markers, ", "), strings.Join(r.UsedBy, ", "))
				}
				if all {
					for _, d := range p.Dirs {
						t.Row(d, "", "")
					}
				}
				if err := t.Flush(); err != nil {
					return err
				}
				g.io.Detail(fmt.Sprintf("%s at %s (%s)", p.Repo, p.Branch, p.Commit[:min(7, len(p.Commit))]))
				if p.Truncated {
					g.io.Warnf("The repository is too big to list whole; some directories are missing")
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "the branch to list (default: the repository's default branch)")
	cmd.Flags().BoolVar(&all, "all", false, "also list directories that don't look like an app")
	return cmd
}

// repoPath checks an owner/name argument and escapes it for a URL path.
func repoPath(repo string) string {
	owner, name, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(repo), "https://github.com/"), ".git"), "/")
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
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
