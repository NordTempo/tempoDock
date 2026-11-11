package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// One-off commands for agents (ADR 0048): run_command runs one without a
// terminal and waits for its end; list_runs shows a service's history.

const (
	defaultRunWait = 300
	maxRunWait     = 900
)

// addRunTools registers the run tools.
func (h *handler) addRunTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "run_command", "Run a command",
		"Run a one-off command (sh -c) in a service's image with its variables and network, and wait. Returns exit code and output.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Command        string `json:"command"`
			TimeoutSeconds *int   `json:"timeout_seconds,omitempty" doc:"Default 300, at most 900."`
			Volumes        string `json:"volumes,omitempty" enum:"none,read,write" doc:"Default read; write snapshots them first."`
			NoSnapshot     bool   `json:"no_snapshot,omitempty"`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			timeout := defaultRunWait
			if in.TimeoutSeconds != nil {
				timeout = *in.TimeoutSeconds
			}
			if timeout < 1 || timeout > maxRunWait {
				return mcpkit.Reply{}, mcpkit.Invalid("timeout_seconds must be between 1 and %d", maxRunWait)
			}
			run, err := h.core.RunCommand(ctx, c.Actor, ref, apitypes.RunCommandRequest{
				Command: in.Command, TimeoutSeconds: timeout, Volumes: apitypes.RunVolumes(in.Volumes), NoSnapshot: in.NoSnapshot,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: runSummary(run), Data: run}, nil
		})

	mcpkit.AddTool(s, "list_runs", "List a service's runs",
		"A service's one-off commands, newest first.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct{ ServiceInput }) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			runs, err := h.core.ListRuns(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d runs of %s.", len(runs), ref.Service), Data: runs}, nil
		})
}

// runSummary says how a run ended in one line.
func runSummary(r apitypes.CommandRun) string {
	took := ""
	if r.FinishedAt != nil {
		took = " after " + r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
	}
	switch {
	case r.ExitCode != nil && r.Cause != "":
		return fmt.Sprintf("Run %d exited with code %d%s. Likely cause: %s.", r.ID, *r.ExitCode, took, r.Cause)
	case r.ExitCode != nil:
		return fmt.Sprintf("Run %d exited with code %d%s.", r.ID, *r.ExitCode, took)
	case r.Error != "":
		return fmt.Sprintf("Run %d %s: %s.", r.ID, r.Status, r.Error)
	}
	return fmt.Sprintf("Run %d %s%s.", r.ID, r.Status, took)
}
