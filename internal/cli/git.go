package cli

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newGitCmd is `dock git`: GitHub push-to-deploy (ADR 0018).
func newGitCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "git", Short: "Deploy a service on every push to a GitHub branch"})
	cmd.AddCommand(newGitConnectCmd(g), newGitStatusCmd(g), newGitDeployCmd(g), newGitDisconnectCmd(g))
	return cmd
}

func newGitConnectCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.GitConnectRequest
	var tokenStdin, removeToken, status bool
	cmd := &cobra.Command{
		Use:   "connect <owner/repo>",
		Short: "Connect a GitHub repository: pushes to the branch deploy the service",
		Long: `Connect the service to a GitHub repository and branch. Every push to the
branch then deploys the service from source, exactly like ` + "`dock deploy`" + ` of the
repository (or of --dir in it): same build, same dock.toml. Pushes to other
branches, tags, and (with --dir) pushes that change nothing in the directory
are ignored. A push that arrives while a deploy runs waits; a newer push
replaces it.

Prints the webhook to add on GitHub, with its secret: shown only now. Run it
again to change the branch, directory, token or statuses; the webhook stays
the same unless --rotate-secret.

Private repositories need a fine-grained token with Contents read access
(and Commit statuses read and write for --status), from stdin. It is stored
encrypted and never shown again.`,
		Example: `  dock git connect acme/shop --branch main
  dock git connect acme/monorepo --branch main --dir apps/web
  printf %s "$GITHUB_TOKEN" | dock git connect acme/shop --branch main --token-stdin --status`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Repo = args[0]
			switch {
			case tokenStdin && removeToken:
				return usageError("pass --token-stdin or --remove-token, not both", "")
			case tokenStdin:
				token, err := g.stdinToken()
				if err != nil {
					return err
				}
				req.Token = &token
			case removeToken:
				req.Token = new(string)
			}
			if cmd.Flags().Changed("status") {
				req.Status = &status
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			res, err := send[apitypes.GitConnectResult](cmd.Context(), s, http.MethodPut, t.servicePath()+"/git", req)
			if err != nil {
				return err
			}
			return g.io.Print(res, func() error {
				conn := res.Connection
				verb := "Updated the GitHub source of"
				if res.Created {
					verb = "Connected"
				}
				g.io.Successf("%s %s: pushes to %s deploy it", verb, t, gitTargetText(conn))
				if res.HooksHint != "" {
					g.io.Warnf("%s", res.HooksHint)
				}
				if res.Secret == "" {
					return g.io.Fields("webhook", dash(conn.WebhookURL), "token", setText(conn.TokenSet), "statuses", onOff(conn.Status))
				}
				_, _ = fmt.Fprintf(g.io.Out, "\nOn GitHub, open %s → Settings → Webhooks → Add webhook and set:\n\n", conn.Repo)
				payload := conn.WebhookURL
				if payload == "" {
					payload = "(none yet: set [hooks] public_host on the server, then run `dock git status`)"
				}
				if err := g.io.Fields(
					"  Payload URL", payload,
					"  Content type", "application/json",
					"  Secret", res.Secret,
					"  SSL verification", "Enable",
					"  Events", "Just the push event",
				); err != nil {
					return err
				}
				_, _ = fmt.Fprintln(g.io.Out, "\nThe secret is not shown again; `dock git connect --rotate-secret` makes a new one.")
				if !conn.TokenSet {
					_, _ = fmt.Fprintln(g.io.Out, "No token: works for public repositories. For a private one, pipe a token with Contents read access to --token-stdin.")
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	fl := cmd.Flags()
	fl.StringVar(&req.Branch, "branch", "main", "the branch whose pushes deploy")
	fl.StringVar(&req.Dir, "dir", "", "subdirectory to build, for monorepos (default: the repository root)")
	fl.BoolVar(&tokenStdin, "token-stdin", false, "read a GitHub token from stdin (private repositories, statuses)")
	fl.BoolVar(&removeToken, "remove-token", false, "remove the stored GitHub token")
	fl.BoolVar(&status, "status", false, "post commit statuses (pending, success, failure) to GitHub; needs a token")
	fl.BoolVar(&req.RotateSecret, "rotate-secret", false, "replace the webhook URL and secret")
	return cmd
}

// stdinToken reads one token from stdin (--token-stdin).
func (g *globals) stdinToken() (string, error) {
	b, err := io.ReadAll(io.LimitReader(g.io.In, maxTokenInput+1))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" || len(b) > maxTokenInput || strings.ContainsAny(token, " \t\r\n") {
		return "", usageError("--token-stdin expects exactly one token on stdin", "Pipe just the token: `printf %s \"$GITHUB_TOKEN\" | dock git connect owner/repo --token-stdin`.")
	}
	return token, nil
}

func newGitStatusCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the service's GitHub source, webhook and latest push",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			src, err := get[apitypes.GitSource](cmd.Context(), s, t.servicePath()+"/git")
			if err != nil {
				return err
			}
			return g.io.Print(src, func() error {
				conn := src.Connection
				if conn == nil {
					g.io.Successf("%s has no GitHub source. Connect one with `dock git connect owner/repo --branch main`.", t)
					return nil
				}
				if src.HooksHint != "" {
					g.io.Warnf("%s", src.HooksHint)
				}
				queued := ""
				if p := conn.Pending; p != nil {
					queued = commitText(*p) + ", waiting for the current deploy"
				}
				last := conn.LastEvent
				if last != "" {
					last += " (" + agoPtr(conn.LastEventAt) + ")"
				}
				return g.io.Fields(
					"source", gitTargetText(*conn),
					"webhook", dash(conn.WebhookURL),
					"token", setText(conn.TokenSet),
					"statuses", onOff(conn.Status),
					"queued", queued,
					"last push", dash(last),
					"connected", ago(conn.CreatedAt)+" by "+conn.CreatedBy,
				)
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

func newGitDeployCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.GitDeployRequest
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy the branch's latest commit (or --ref) from GitHub now",
		Long: `Queue a commit of the connected repository as if it had been pushed: the
branch's head, or --ref (a commit SHA, branch or tag). It deploys even if
it is already live, so it also retries a failed push. Its release starts as
soon as no other deploy of the service runs; follow it with ` + "`dock status`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			res, err := send[apitypes.GitDeployResult](cmd.Context(), s, http.MethodPost, t.servicePath()+"/git/deploy", req)
			if err != nil {
				return err
			}
			return g.io.Print(res, func() error {
				g.io.Successf("Queued %s for %s; follow it with `dock status` or `dock releases`", commitText(res.Commit), t)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&req.Ref, "ref", "", "commit SHA, branch or tag (default: the connected branch)")
	return cmd
}

func newGitDisconnectCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "disconnect",
		Short: "Stop deploying on pushes: remove the GitHub source and its token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Disconnect %s from GitHub? Its webhook stops working; releases are kept.", t), "yes"); err != nil {
					return err
				}
			}
			if err := del(cmd.Context(), s, t.servicePath()+"/git"); err != nil {
				return err
			}
			return g.io.Print(struct {
				Disconnected bool `json:"disconnected"`
			}{true}, func() error {
				g.io.Successf("Disconnected %s; delete the webhook on GitHub too", t)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// gitTargetText is "acme/shop@main" plus " in apps/web/".
func gitTargetText(c apitypes.GitConnection) string {
	s := c.Repo + "@" + c.Branch
	if c.Dir != "" {
		s += " in " + c.Dir + "/"
	}
	return s
}

// commitText is "abc1234 fix checkout (ada)".
func commitText(c apitypes.Commit) string {
	s := c.SHA[:min(7, len(c.SHA))]
	if c.Message != "" {
		s += " " + c.Message
	}
	if c.Author != "" {
		s += " (" + c.Author + ")"
	}
	return s
}

func setText(set bool) string {
	if set {
		return "set"
	}
	return "not set"
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
