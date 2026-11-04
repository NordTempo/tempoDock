package cli

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Shell completion of names: projects, environments, services and release
// numbers, read from the API. Completions run only under cobra's hidden
// __complete command (which `dock completion <shell>` scripts call), so
// they cost nothing at start-up. They resolve the server, organization and
// target like commands do, narrowed by the -p and -e already typed, and
// fail silent: offline, logged out or not found completes nothing.

// completionTimeout bounds the API calls one completion makes, so a server
// that doesn't answer never hangs the shell.
const completionTimeout = 3 * time.Second

// isCompletion reports whether args run cobra's completion command.
func isCompletion(args []string) bool {
	return len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd)
}

// flagCompletions are the completions of flags by name, wherever they are
// defined: every --project, --environment, --service and --release names
// the same kind of thing.
var flagCompletions = map[string]cobra.CompletionFunc{
	"project":     completeProjects,
	"environment": completeEnvironments,
	"service":     completeServices,
	"release":     completeReleases,
}

// registerCompletions gives every flag in root's tree that flagCompletions
// names its completion, unless the command registered its own. Run only
// for __complete.
func registerCompletions(root *cobra.Command) {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for name, fn := range flagCompletions {
			if c.Flags().Lookup(name) == nil {
				continue
			}
			if _, ok := c.GetFlagCompletionFunc(name); !ok {
				_ = c.RegisterFlagCompletionFunc(name, fn)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// needServer is the scope of a completion that resolves no target: listing
// projects needs only the server.
const needServer scope = -1

// completer lists completions for the target t the command line names so
// far, through s; args are the positional arguments typed before.
type completer func(ctx context.Context, s *session, t target, args []string) ([]cobra.Completion, error)

// completion turns list into a cobra completion func: it connects,
// resolves a target at level sc from cmd's flags and the directory's link
// (none for needServer), and keeps the completions that start with what
// was typed. For flags, positional is 0; for arguments, it is how many of
// the first ones it completes.
func completion(sc scope, positional int, list completer) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		const none = cobra.ShellCompDirectiveNoFileComp
		if positional > 0 && len(args) >= positional {
			return nil, none
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
		defer cancel()
		g := &globals{ctx: ctx, org: stringFlag(cmd, "org")}
		f := targetFlags{project: stringFlag(cmd, "project"), environment: stringFlag(cmd, "environment"), service: stringFlag(cmd, "service")}
		var s *session
		var t target
		var err error
		if sc == needServer {
			s, err = g.connect()
		} else {
			s, t, err = g.open(f, sc)
		}
		if err != nil {
			return nil, none
		}
		all, err := list(ctx, s, t, args)
		if err != nil {
			return nil, none
		}
		return slices.DeleteFunc(all, func(c cobra.Completion) bool { return !strings.HasPrefix(c, toComplete) }), none | cobra.ShellCompDirectiveKeepOrder
	}
}

// stringFlag is the value of cmd's string flag name, "" when it has none.
func stringFlag(cmd *cobra.Command, name string) string {
	if f := cmd.Flags().Lookup(name); f != nil && f.Value.Type() == "string" {
		return f.Value.String()
	}
	return ""
}

// listNames gets the API objects at path and returns their names.
func listNames[T any](ctx context.Context, s *session, path string, name func(T) string) ([]cobra.Completion, error) {
	items, err := get[[]T](ctx, s, path)
	if err != nil {
		return nil, err
	}
	return names(items, name), nil
}

// completeProjects completes the organization's project names.
var completeProjects = completion(needServer, 0, listProjects)

// completeEnvironments completes the environments of -p (or the linked
// project).
var completeEnvironments = completion(needProject, 0, listEnvironments)

// completeServices completes the services of -p and -e (or the linked
// environment).
var completeServices = completion(needEnvironment, 0, listServices)

// completeReleases completes release numbers of the target's service,
// newest first, described by status and source.
var completeReleases = completion(needService, 0, listReleases)

// Positional arguments: the first argument of commands like `release show
// <number>`, `service show [name]` and `project archive <name>`, and both
// of `promote <from> <to>`.
var (
	completeReleaseArg      = completion(needService, 1, listReleases)
	completeProjectArg      = completion(needServer, 1, listProjects)
	completeEnvironmentArg  = completion(needProject, 1, listEnvironments)
	completeEnvironmentArgs = completion(needProject, 2, listEnvironments)
	completeServiceArg      = completion(needEnvironment, 1, listServices)
)

// listProjects lists the project names the caller can see.
func listProjects(ctx context.Context, s *session, _ target, _ []string) ([]cobra.Completion, error) {
	return listNames(ctx, s, apiPrefix+"/projects", func(p apitypes.Project) string { return p.Name })
}

// listEnvironments lists t's project's environment names.
func listEnvironments(ctx context.Context, s *session, t target, _ []string) ([]cobra.Completion, error) {
	return listNames(ctx, s, t.projectPath()+"/environments", func(e apitypes.Environment) string { return e.Name })
}

// listServices lists the names of t's environment's live services.
func listServices(ctx context.Context, s *session, t target, _ []string) ([]cobra.Completion, error) {
	svcs, err := get[[]apitypes.Service](ctx, s, t.envPath()+"/services")
	if err != nil {
		return nil, err
	}
	return names(live(svcs), func(svc apitypes.Service) string { return svc.Name }), nil
}

// listReleases lists t's newest releases as "12<tab>active upload".
func listReleases(ctx context.Context, s *session, t target, _ []string) ([]cobra.Completion, error) {
	return listNames(ctx, s, t.servicePath()+"/releases?limit=30", func(r apitypes.Release) string {
		return cobra.CompletionWithDesc(itoa(r.Number), string(r.Status)+" "+describeSource(r))
	})
}

// listFromServices lists the services of the environment named by the first
// argument, `dock promote <from>`'s.
func listFromServices(ctx context.Context, s *session, t target, args []string) ([]cobra.Completion, error) {
	if len(args) == 0 {
		return nil, nil
	}
	t.Environment = args[0]
	return listServices(ctx, s, t, args)
}

// Numbers and names of other things commands take as their argument.
var (
	completeRequestArg  = completion(needServer, 1, listRequests)
	completeRunArg      = completion(needService, 1, listRuns)
	completeSnapshotArg = completion(needService, 1, listSnapshots)
	completeDrainArg    = completion(needEnvironment, 1, listDrains)
)

// listRequests lists the organization's access requests, newest first, as
// "12<tab>pending deploy web in production".
func listRequests(ctx context.Context, s *session, _ target, _ []string) ([]cobra.Completion, error) {
	return listNames(ctx, s, apiPrefix+"/requests", func(r apitypes.AccessRequest) string {
		return cobra.CompletionWithDesc(itoa(r.ID), string(r.Status)+" "+r.Summary)
	})
}

// listRuns lists t's latest one-off runs as "7<tab>succeeded bin/migrate".
func listRuns(ctx context.Context, s *session, t target, _ []string) ([]cobra.Completion, error) {
	return listNames(ctx, s, t.servicePath()+"/runs", func(r apitypes.CommandRun) string {
		return cobra.CompletionWithDesc(itoa(r.ID), string(r.Status)+" "+cut(oneLine(r.Command), 40))
	})
}

// listSnapshots lists t's volume snapshots as "3<tab>ready manual".
func listSnapshots(ctx context.Context, s *session, t target, _ []string) ([]cobra.Completion, error) {
	ss, err := get[apitypes.ServiceSnapshots](ctx, s, t.servicePath()+"/snapshots")
	if err != nil {
		return nil, err
	}
	return names(ss.Snapshots, func(sn apitypes.Snapshot) string {
		return cobra.CompletionWithDesc(itoa(sn.ID), string(sn.Status)+" "+string(sn.Trigger))
	}), nil
}

// listDrains lists the names of t's environment's log drains.
func listDrains(ctx context.Context, s *session, t target, _ []string) ([]cobra.Completion, error) {
	return listNames(ctx, s, drainsPath(t), func(d apitypes.LogDrain) string { return d.Name })
}
