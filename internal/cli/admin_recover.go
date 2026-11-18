//go:build !dockcli

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
)

// newAdminRecoverCmd is `dock admin recover`: what comes back on a
// control plane restored onto a server that replaced a lost one (ADR
// 0098). It talks to the running daemon over its loopback listener and
// proves itself with the token in the data dir's recovery file, so it
// needs no sign-in: shell access to the server is the proof.
func newAdminRecoverCmd() *cobra.Command {
	configPath := config.DefaultPath
	var done, asJSON bool
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Show what a recovery restores on a server that replaced a lost one (root)",
		Long: `After install.sh --restore-from-bucket the control plane is back, in
recovery mode: the apps and their volumes are not, and scheduled backups
wait, so an empty volume is never backed up over a good one.

This prints the plan and changes nothing: every stateful service on this
server with its newest backup and how old it is, and what has none. A
service whose backups were only in a server's local repository lost them
with the server.

--done ends recovery mode: scheduled backups run again.

Run it on the server, as root or the dock user, while dock runs.`,
		Example: `  sudo dock admin recover
  sudo dock admin recover --json
  sudo dock admin recover --done`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			rec, err := cpbackup.ReadRecovery(cfg.DataDir)
			if errors.Is(err, fs.ErrPermission) {
				return &output.Error{Exit: output.ExitAuth, Err: err, Hint: "Run it as root (sudo) or as the dock user."}
			}
			if err != nil {
				return err
			}
			if rec == nil {
				return &output.Error{Exit: output.ExitConflict, Err: errors.New("this control plane is not in recovery mode"),
					Hint: "Recovery mode starts with install.sh --restore-from-bucket. To restore one service's backup: dock backup restore."}
			}
			base := "http://" + cfg.InternalListen + api.RecoverPath
			if done {
				if err := recoverCall(cmd.Context(), http.MethodPost, base+"done", rec.Token, nil); err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Recovery mode ended: scheduled backups run again.")
				return err
			}
			var plan apitypes.RecoveryPlan
			if err := recoverCall(cmd.Context(), http.MethodGet, base+"plan", rec.Token, &plan); err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(plan)
			}
			return printRecoveryPlan(cmd.OutOrStdout(), plan)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", configPath, "path to the daemon config file")
	cmd.Flags().BoolVar(&done, "done", false, "end recovery mode: scheduled backups run again")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the plan as JSON")
	cmd.MarkFlagsMutuallyExclusive("done", "json")
	return cmd
}

// recoverCall sends one request to the daemon's recovery endpoints and
// decodes the answer into out (nil: none expected).
func recoverCall(ctx context.Context, method, url, token string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return &output.Error{Exit: output.ExitUnreachable, Err: err, Hint: "dock must be running: sudo systemctl start dock, then run this again."}
	}
	defer func() { _ = res.Body.Close() }()
	switch {
	case res.StatusCode == http.StatusForbidden:
		return &output.Error{Exit: output.ExitConflict, Err: errors.New("the running dock is not in recovery mode"),
			Hint: "It read the recovery file when it started: sudo systemctl restart dock, then run this again."}
	case res.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return fmt.Errorf("dock answered %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// printRecoveryPlan prints a plan as a table, with what it means below.
func printRecoveryPlan(w io.Writer, plan apitypes.RecoveryPlan) error {
	_, _ = fmt.Fprintf(w, "Restored from %s.\nBackups are read from: %s\n\n", plan.From, plan.Repository)
	if len(plan.Services) == 0 {
		_, err := fmt.Fprintln(w, "No service on this server has a volume: nothing to restore. Deploy the services, then: sudo dock admin recover --done")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SERVICE\tKIND\tPLAN\tBACKUP\tTAKEN\tSIZE")
	var restore, lost, none int
	for _, s := range plan.Services {
		name := strings.Join([]string{s.Org, s.Project, s.Environment, s.Service}, "/")
		switch s.State {
		case apitypes.RecoveryRestore:
			restore++
			_, _ = fmt.Fprintf(tw, "%s\t%s\trestore\t%s\t%s\t%s\n", name, s.Kind, s.Snapshot[:min(8, len(s.Snapshot))], agoPtr(s.BackedUpAt), bytesize(s.SizeBytes))
		case apitypes.RecoveryLost:
			lost++
			_, _ = fmt.Fprintf(tw, "%s\t%s\tlost\t\t\t\n", name, s.Kind)
		default:
			none++
			_, _ = fmt.Fprintf(tw, "%s\t%s\tno backup\t\t\t\n", name, s.Kind)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "\n%d to restore", restore)
	if lost > 0 {
		_, _ = fmt.Fprintf(w, ", %d lost (backed up only into a repository that is not this one, such as the lost server's local one)", lost)
	}
	if none > 0 {
		_, _ = fmt.Fprintf(w, ", %d never backed up", none)
	}
	_, err := fmt.Fprintln(w, ". Lost and never backed up services start with empty volumes.")
	return err
}
