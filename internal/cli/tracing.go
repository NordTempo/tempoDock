package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Tracing over OTLP: the platform switch, for operators. Turning it on
// starts the trace store beside the analytics store, on the analytics
// server.

func newTracingCmd(g *globals) *cobra.Command {
	show := func(cmd *cobra.Command, set *bool) error {
		s, err := g.connect()
		if err != nil {
			return err
		}
		var st apitypes.TracingStatus
		if set == nil {
			st, err = get[apitypes.TracingStatus](cmd.Context(), s, apiPrefix+"/tracing")
		} else {
			st, err = send[apitypes.TracingStatus](cmd.Context(), s, http.MethodPut, apiPrefix+"/tracing", apitypes.TracingSettings{Enabled: *set})
		}
		if err != nil {
			return err
		}
		return g.io.Print(st, func() error { return printTracing(g, st) })
	}
	cmd := &cobra.Command{
		Use:   "tracing",
		Short: "Tracing over OTLP on the platform: on or off, and the trace store (operators)",
		Long: `Whether tracing is on, and the trace store: spans kept by Dock in a
container of its own beside the analytics store, on the analytics server
(` + "`dock analytics server`" + `), for [traces] retention_days.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return show(cmd, nil) },
	}
	on := true
	cmd.AddCommand(&cobra.Command{
		Use: "on", Args: cobra.NoArgs,
		Short: "Turn tracing on: start the trace store",
		RunE:  func(cmd *cobra.Command, _ []string) error { return show(cmd, &on) },
	})
	var yes bool
	offCmd := &cobra.Command{
		Use: "off", Args: cobra.NoArgs,
		Short: "Turn tracing off: remove the trace store and every span it holds",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				if err := g.io.Confirm("Turn tracing off? The trace store and every span it holds are removed.", "yes"); err != nil {
					return err
				}
			}
			off := false
			return show(cmd, &off)
		},
	}
	offCmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.AddCommand(offCmd)
	var sure bool
	rotate := &cobra.Command{
		Use: "rotate-key", Args: cobra.NoArgs,
		Short: "Revoke every trace ingest token: rotate the trace key",
		Long: `Rotates the trace key, so every ingest token minted before stops working
at once: what to do when a token leaked. Traced services get new tokens
when their containers start, with their next deploy or restart; until
then Dock refuses their spans.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !sure {
				if err := g.io.Confirm("Rotate the trace key? Traced apps send nothing Dock keeps until their next deploy or restart.", "yes"); err != nil {
					return err
				}
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := send[apitypes.TracingStatus](cmd.Context(), s, http.MethodPost, apiPrefix+"/tracing/rotate-key", nil)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("Rotated the trace key. Restart or redeploy traced services to give them new tokens.")
				return nil
			})
		},
	}
	rotate.Flags().BoolVarP(&sure, "yes", "y", false, "don't ask for confirmation")
	cmd.AddCommand(rotate)
	return cmd
}

func printTracing(g *globals, st apitypes.TracingStatus) error {
	state := "off"
	if st.Enabled {
		state = "on"
	}
	store := "not running"
	switch {
	case st.StoreRunning:
		store = fmt.Sprintf("running (%s), %s", st.StoreVersion, bytesize(st.StoreBytes))
	case st.Enabled && !st.ServerOnline:
		store = "unreachable: " + st.Server + " is offline"
	case st.Starting:
		store = "starting (the first start downloads it)"
	case st.Enabled:
		store = "not answering; see `docker logs dock-traces` on " + st.Server
	}
	rotated := ""
	if st.KeyRotatedAt != nil {
		rotated = st.KeyRotatedAt.Local().Format("Jan 2 2006 15:04")
	}
	return g.io.Fields("Tracing", state, "Server", st.Server, "Trace store", store, "Problem", st.Error,
		"Keeps", fmt.Sprintf("%d days of spans, in %d MB of memory", st.RetentionDays, st.MemoryMB),
		"Key rotated", rotated)
}
