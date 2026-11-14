package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// apiPrefix is the base path of every API operation.
const apiPrefix = "/api/" + apitypes.APIVersion

// session is what an API command works with: a client for the resolved
// server and the current directory's link, if any.
type session struct {
	api    *apiclient.Client
	cfg    userConfig
	server clientconfig.Resolved
	link   *clientconfig.Link
}

// connect resolves the server for the current directory and fails early,
// with what to do, when there is no token to send.
func (g *globals) connect() (*session, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	_, link := cfg.currentLink()
	r, err := cfg.resolveFor(link)
	if err != nil {
		return nil, err
	}
	if err := checkTokens(r); err != nil {
		return nil, err
	}
	if r.Token == "" && r.PlatformToken != "" {
		// Only a platform login here: it runs the platform's commands.
		r.Token = r.PlatformToken
	}
	if r.Token == "" {
		return nil, &output.Error{
			Exit:    output.ExitAuth,
			Message: "no API token for " + r.Server,
			Hint:    fmt.Sprintf("Run `dock login %s`, or set %s and %s.", r.Server, clientconfig.EnvURL, clientconfig.EnvToken),
		}
	}
	if g.org != "" {
		r.Org = g.org
	}
	api, err := apiclient.New(r.Server, r.Token)
	if err != nil {
		return nil, err
	}
	api.Org, api.Reason = r.Org, g.reason
	api.SetPlatformToken(r.PlatformToken)
	if api.Remote() && !g.io.JSON { // warnings are silent with --json
		to := r.Server + " (" + clientconfig.EnvURL + ")"
		if r.Name != "" {
			to = r.Name + " (" + r.Server + ")"
		}
		api.BeforeChange = func() { _, _ = fmt.Fprintln(g.io.Err, "→", to) }
	}
	return &session{api: api, cfg: cfg, server: r, link: link}, nil
}

// checkTokens refuses a token with whitespace or control characters in
// it (a pasted newline, say), which HTTP can't send: it says which token
// and where it came from, instead of failing to reach the server.
func checkTokens(r clientconfig.Resolved) error {
	from := "the token of context " + r.Name + " (dock login stores it in the config file)"
	platformFrom := "the platform login of context " + r.Name
	if r.Source == clientconfig.SourceEnvURL {
		from, platformFrom = clientconfig.EnvToken, clientconfig.EnvPlatformToken
	}
	for _, t := range []struct{ token, from string }{{r.Token, from}, {r.PlatformToken, platformFrom}} {
		if strings.IndexFunc(t.token, func(c rune) bool { return c <= ' ' || c == 0x7f }) >= 0 {
			return &output.Error{
				Exit:    output.ExitUsage,
				Message: "malformed token in " + t.from + ": it has a space, newline or control character",
				Hint:    "Set it again without them, like `printf %s \"$TOKEN\" | dock login <url> --token-stdin`, or fix the environment variable.",
			}
		}
	}
	return nil
}

// get decodes a GET response into a T.
func get[T any](ctx context.Context, s *session, path string) (T, error) {
	return apiclient.Get[T](ctx, s.api, path)
}

// send sends in as JSON with method and decodes the response into a T.
func send[T any](ctx context.Context, s *session, method, path string, in any) (T, error) {
	var out T
	err := s.api.JSON(ctx, method, path, in, &out)
	return out, err
}

// del sends a DELETE that returns no body.
func del(ctx context.Context, s *session, path string) error {
	return s.api.JSON(ctx, http.MethodDelete, path, nil, nil)
}

// target is the project, environment and service a command acts on.
type target struct {
	Project, Environment, Service string
}

func (t target) projectPath() string { return apiPrefix + "/projects/" + url.PathEscape(t.Project) }

func (t target) envPath() string {
	return t.projectPath() + "/environments/" + url.PathEscape(t.Environment)
}

func (t target) servicePath() string {
	return t.envPath() + "/services/" + url.PathEscape(t.Service)
}

// String is "project/environment[/service]".
func (t target) String() string {
	parts := []string{t.Project, t.Environment}
	if t.Service != "" {
		parts = append(parts, t.Service)
	}
	return strings.Join(parts, "/")
}

// flags renders t as the -p, -e and -s flags that address it ("-p shop -e
// production -s web"), for hints that name a command to run next: with them
// the command works outside a linked directory too. Names are DNS-safe, so
// they need no quoting.
func (t target) flags() string {
	parts := make([]string, 0, 6)
	for _, f := range [...]struct{ flag, name string }{{"-p", t.Project}, {"-e", t.Environment}, {"-s", t.Service}} {
		if f.name != "" {
			parts = append(parts, f.flag, f.name)
		}
	}
	return strings.Join(parts, " ")
}

// releaseTarget is the service r belongs to.
func releaseTarget(r apitypes.Release) target {
	return target{r.Project, r.Environment, r.Service}
}

// scope says how much of a target a command needs.
type scope int

const (
	needProject scope = iota
	needEnvironment
	needService
)

// targetFlags are --project, --environment and --service, which override
// the directory's link.
type targetFlags struct {
	project, environment, service string
}

// add registers the flags a command at level s understands.
func (f *targetFlags) add(cmd *cobra.Command, s scope) {
	cmd.Flags().StringVarP(&f.project, "project", "p", "", "project (default: the linked one)")
	if s >= needEnvironment {
		cmd.Flags().StringVarP(&f.environment, "environment", "e", "", "environment (default: the linked one)")
	}
	if s >= needService {
		cmd.Flags().StringVarP(&f.service, "service", "s", "", "service (default: the linked one)")
	}
}

// resolve merges the flags with link. Naming another project than the
// linked one drops the linked environment and service; naming another
// environment keeps the linked service name, so `-e staging` means "this
// service, in staging".
func (f targetFlags) resolve(link *clientconfig.Link, s scope) (target, error) {
	var t target
	if link != nil {
		t = target{link.Project.Name, link.Environment.Name, link.Service.Name}
	}
	if f.project != "" && f.project != t.Project {
		t = target{Project: f.project}
	}
	if f.environment != "" {
		t.Environment = f.environment
	}
	if f.service != "" {
		t.Service = f.service
	}
	switch {
	case t.Project == "":
		return t, noTarget("project", "Pass --project, or link this directory with `dock link`.")
	case s >= needEnvironment && t.Environment == "":
		return t, noTarget("environment", "Pass --environment, or link this directory with `dock link`.")
	case s >= needService && t.Service == "":
		return t, noTarget("service", "Pass --service, or link this directory to a service with `dock link --service <name>`.")
	}
	return t, nil
}

func noTarget(what, hint string) error {
	return &output.Error{Exit: output.ExitUsage, Message: "no " + what + " selected; missing --" + what, Hint: hint}
}

// open connects and resolves the target in one step, which is how most
// commands start.
func (g *globals) open(f targetFlags, s scope) (*session, target, error) {
	sess, err := g.connect()
	if err != nil {
		return nil, target{}, err
	}
	if f.project == "" {
		if err := sess.refreshLinkedProject(g.ctx); err != nil {
			return nil, target{}, err
		}
		if sess.link != nil {
			sess.api.ProjectID = sess.link.Project.ID
		}
	}
	t, err := f.resolve(sess.link, s)
	return sess, t, err
}

// refreshLinkedProject resolves a directory by stable ID, never by its cached
// name. A missing identity fails closed, even if another project reused its name.
func (s *session) refreshLinkedProject(ctx context.Context) error {
	if s.link == nil {
		return nil
	}
	if s.link.Project.ID == "" {
		return noTarget("project identity in this directory's old link", "Link this directory again with `dock link -p <project>`.")
	}
	p, err := get[apitypes.Project](ctx, s, apiPrefix+"/project-ids/"+url.PathEscape(s.link.Project.ID))
	if err != nil {
		if e, ok := errors.AsType[*output.Error](err); ok {
			copy := *e
			copy.Hint = "Check the project and organization, then link this directory again with `dock link -p <project>`."
			return &copy
		}
		return err
	}
	link := *s.link
	link.Project.Name = p.Name
	s.link = &link
	return nil
}
