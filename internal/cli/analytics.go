package cli

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Dock Analytics (ADR 0037): the platform side, for operators. Sites and
// their numbers come with `dock analytics` subcommands of their own.

func newAnalyticsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Dock Analytics: cookieless web analytics for your sites",
		Long: `Show Dock Analytics' state: whether it is on, which server runs the
analytics store, and whether the store answers (operator).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			st, err := get[apitypes.AnalyticsStatus](cmd.Context(), s, apiPrefix+"/analytics")
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error { return printAnalytics(g, st) })
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "server [<server>]",
		Short: "Choose the server the analytics store runs on, and turn analytics on (operator)",
		Long: `Run the analytics store on <server>, or on the control plane's own server
without one, and turn analytics on. Every server keeps its events until
the store has them, so a store that is off for a while loses nothing.

A store already running on another server stops; its data stays there,
and the new store starts empty.`,
		Example: "  dock analytics server homelab\n  dock analytics server",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			req := apitypes.SetAnalyticsServerRequest{}
			if len(args) == 1 {
				req.Server = args[0]
			}
			st, err := send[apitypes.AnalyticsStatus](cmd.Context(), s, http.MethodPut, apiPrefix+"/analytics/server", req)
			if err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("The analytics store runs on %s", st.Server)
				return printAnalytics(g, st)
			})
		},
	})
	return cmd
}

// printAnalytics prints the state as fields.
func printAnalytics(g *globals, st apitypes.AnalyticsStatus) error {
	state := "off"
	if st.Enabled {
		state = "on"
	}
	store := "not running"
	switch {
	case st.StoreRunning:
		store = fmt.Sprintf("running (%s), %s events, %s", st.StoreVersion, strconv.FormatInt(st.Events, 10), bytesize(st.StoreBytes))
	case st.Enabled && !st.ServerOnline:
		store = "unreachable: " + st.Server + " is offline; events wait in each server's spool"
	case st.Enabled:
		store = "starting, or not answering; see `docker logs dock-analytics` on " + st.Server
	}
	return g.io.Fields("Analytics", state, "Server", st.Server, "Store", store)
}
