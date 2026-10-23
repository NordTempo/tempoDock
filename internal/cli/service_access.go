package cli

import (
	"bufio"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Access protection (ADR 0030): who can open a service's domains.

func newAccessCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Who can open a service's domains: everyone, with a password, or with a Dock login",
		Long: `Show who can open the service's domains. It covers every domain of the
service, generated ones included, and changes take effect at once:

  public    anyone (the default)
  password  visitors type a username and password (HTTP basic auth)
  login     members signed in to Dock who can read the service; Dock
            sends visitors to sign in and back, and tells the app who
            they are in the X-Dock-User header`,
		Example: `  dock access
  dock access password --username team
  dock access login
  dock access public`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			a, err := get[apitypes.Access](cmd.Context(), s, t.servicePath()+"/access")
			if err != nil {
				return err
			}
			return g.io.Print(a, func() error {
				g.io.Successf("%s: %s", t, describeAccess(a))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.AddCommand(newAccessSetCmd(g, apitypes.AccessPublic), newAccessPasswordCmd(g), newAccessSetCmd(g, apitypes.AccessLogin))
	return cmd
}

// describeAccess says who can open a service, as `dock access` prints it.
func describeAccess(a apitypes.Access) string {
	switch a.Mode {
	case apitypes.AccessPassword:
		return "visitors need the password for " + a.Username
	case apitypes.AccessLogin:
		return "visitors need to sign in to Dock and be able to read the service"
	}
	return "anyone can open its domains"
}

// newAccessSetCmd is `dock access public` and `dock access login`.
func newAccessSetCmd(g *globals, mode apitypes.AccessMode) *cobra.Command {
	var f targetFlags
	short := "Let anyone open the service's domains"
	if mode == apitypes.AccessLogin {
		short = "Only let members signed in to Dock open the service's domains"
	}
	cmd := &cobra.Command{
		Use:   string(mode),
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return g.setAccess(cmd, f, apitypes.SetAccessRequest{Mode: mode})
		},
	}
	f.add(cmd, needService)
	return cmd
}

func newAccessPasswordCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.SetAccessRequest
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "password --username <name>",
		Short: "Ask visitors for a username and password",
		Long: `Ask visitors of the service's domains for a username and password (HTTP
basic auth). The password is at least 12 characters, prompted for twice,
or read from stdin with --password-stdin. Dock keeps only its hash.

On a service that already has a password, an empty one keeps it, so
--username alone changes the username.`,
		Example: `  dock access password --username team
  printf %s "$PASSWORD" | dock access password --username team --password-stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req.Mode = apitypes.AccessPassword
			var err error
			if req.Password, err = g.newAccessPassword(fromStdin); err != nil {
				return err
			}
			return g.setAccess(cmd, f, req)
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&req.Username, "username", "", "the username visitors type")
	cmd.Flags().BoolVar(&fromStdin, "password-stdin", false, "read the password from stdin")
	_ = cmd.MarkFlagRequired("username")
	return cmd
}

// newAccessPassword reads the password from stdin or prompts for it twice.
func (g *globals) newAccessPassword(fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(io.LimitReader(g.io.In, maxPasswordInput)).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	p, err := g.io.PromptSecret("Password (empty keeps the current one)", "password-stdin")
	if err != nil || p == "" {
		return p, err
	}
	again, err := g.io.PromptSecret("Password again", "password-stdin")
	if err != nil {
		return "", err
	}
	if again != p {
		return "", usageError("the passwords don't match", "Run `dock access password` again and type the same password twice.")
	}
	return p, nil
}

// setAccess sends a change of access and prints the result.
func (g *globals) setAccess(cmd *cobra.Command, f targetFlags, req apitypes.SetAccessRequest) error {
	s, t, err := g.open(f, needService)
	if err != nil {
		return err
	}
	a, err := send[apitypes.Access](cmd.Context(), s, http.MethodPut, t.servicePath()+"/access", req)
	if err != nil {
		return err
	}
	return g.io.Print(a, func() error {
		g.io.Successf("%s: %s", t, describeAccess(a))
		return nil
	})
}
