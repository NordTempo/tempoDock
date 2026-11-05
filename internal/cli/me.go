package cli

import (
	"bufio"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Your own account.

func newMeCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "me",
		Short: "Your own account",
	})
	cmd.AddCommand(newMeNameCmd(g), newMePasswordCmd(g), newMeTwoFactorCmd(g))
	return cmd
}

// newMeNameCmd changes the caller's display name (PATCH /me).
func newMeNameCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "name <name>",
		Short: "Change your display name",
		Long: `Change the name your account shows: 1 to 64 characters, trimmed. Works
with the token from a browser ` + "`dock login`" + `, which acts for you; API
tokens have no account.`,
		Example: `  dock me name "Ada Lovelace"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			me, err := send[apitypes.Me](cmd.Context(), s, http.MethodPatch, apiPrefix+"/me", apitypes.UpdateMeRequest{Name: args[0]})
			if err != nil {
				return err
			}
			return g.io.Print(me, func() error {
				g.io.Successf("Your name is %s now", me.DisplayName)
				return nil
			})
		},
	}
}

// maxPasswordInput bounds what --password-stdin reads.
const maxPasswordInput = 4 << 10

func newMePasswordCmd(g *globals) *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Change your password",
		Long: `Change your own password: the current one, then the new one twice (at
least 12 characters). Works with the token from a browser ` + "`dock login`" + `,
which acts for you; API tokens have no password. All your UI sessions and
other ` + "`dock login`" + ` tokens end; this CLI's token stays valid.

--password-stdin reads two lines instead: the current password, then the
new one.`,
		Example: `  dock me password
  printf '%s\n%s\n' "$OLD" "$NEW" | dock me password --password-stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := g.passwordChange(fromStdin)
			if err != nil {
				return err
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := s.api.JSON(cmd.Context(), http.MethodPost, apiPrefix+"/me/password", req, nil); err != nil {
				return err
			}
			v := struct {
				Changed bool `json:"changed"`
			}{true}
			return g.io.Print(v, func() error {
				g.io.Successf("Changed your password; your UI sessions and other dock login tokens ended")
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "password-stdin", false, "read the current and the new password from stdin, one per line")
	return cmd
}

// passwordChange reads the current and new password from stdin
// (--password-stdin) or prompts for them on a terminal, the new one twice.
func (g *globals) passwordChange(fromStdin bool) (apitypes.ChangePasswordRequest, error) {
	var req apitypes.ChangePasswordRequest
	if fromStdin {
		lines := bufio.NewScanner(io.LimitReader(g.io.In, maxPasswordInput))
		for _, p := range []*string{&req.CurrentPassword, &req.NewPassword} {
			if lines.Scan() {
				*p = strings.TrimRight(lines.Text(), "\r")
			}
		}
		if req.CurrentPassword == "" || req.NewPassword == "" {
			return req, usageError("--password-stdin expects two lines: the current password, then the new one",
				"Pipe both: `printf '%s\\n%s\\n' \"$OLD\" \"$NEW\" | dock me password --password-stdin`.")
		}
		return req, nil
	}
	var err error
	if req.CurrentPassword, err = g.io.PromptSecret("Current password", "password-stdin"); err != nil {
		return req, err
	}
	if req.NewPassword, err = g.io.PromptSecret("New password", "password-stdin"); err != nil {
		return req, err
	}
	again, err := g.io.PromptSecret("New password again", "password-stdin")
	if err != nil {
		return req, err
	}
	if again != req.NewPassword {
		return req, usageError("the new passwords don't match", "Run `dock me password` again and type the same new password twice.")
	}
	return req, nil
}
