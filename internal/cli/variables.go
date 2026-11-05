package cli

import (
	"bufio"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func newEnvCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{
		Use:   "env",
		Short: "Manage variables (staged until applied)",
		Long: `Variables are set per service, or shared by every service in an environment
(--shared). Changes are staged: they go live with ` + "`dock apply`" + `, with --deploy,
or (for a service's own variables) with its next ` + "`dock deploy`" + `.

Values may reference other variables: ${{shared.KEY}} or ${{service.KEY}}.
(` + "`dock environment`" + ` manages environments; ` + "`dock env clone`" + `, ` + "`lock`" + `, ` + "`unlock`" + ` and ` + "`previews`" + ` are
` + "`dock environment`" + `'s.)`,
	})
	cmd.AddCommand(newEnvListCmd(g), newEnvSetCmd(g), newEnvUnsetCmd(g), newEnvImportCmd(g), newEnvironmentCloneCmd(g),
		newEnvironmentLockCmd(g), newEnvironmentUnlockCmd(g), newPreviewsCmd(g))
	return cmd
}

func newEnvListCmd(g *globals) *cobra.Command {
	var f targetFlags
	var reveal bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List variables: shared ones and the service's (or every service's)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			q := url.Values{}
			if t.Service != "" {
				q.Set("service", t.Service)
			}
			if reveal {
				q.Set("reveal", "true")
			}
			vars, err := get[[]apitypes.Variable](cmd.Context(), s, t.envPath()+"/variables?"+q.Encode())
			if err != nil {
				return err
			}
			return g.io.Print(vars, func() error {
				if len(vars) == 0 {
					g.io.Successf("No variables in %s", t)
					return nil
				}
				headers := []string{"key", "scope", "pending"}
				if reveal {
					headers = append(headers, "value")
				}
				tb := g.io.Table(headers...)
				for _, v := range vars {
					scope := "shared"
					if v.Scope == apitypes.ScopeService {
						scope = v.Service
					}
					row := []string{v.Key, scope, dash(string(v.Pending))}
					if reveal {
						row = append(row, deref(v.Value))
					}
					tb.Row(row...)
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVar(&reveal, "reveal", false, "show live values (admin)")
	return cmd
}

// varTarget is the shared flag set of set, unset and import.
type varTarget struct {
	targetFlags
	shared, deploy, detach bool
}

func (v *varTarget) add(cmd *cobra.Command) {
	v.targetFlags.add(cmd, needService)
	cmd.Flags().BoolVar(&v.shared, "shared", false, "environment-level variables shared by every service")
	addDeployFlags(cmd, &v.deploy, &v.detach)
}

// stageVariables sends one SetVariablesRequest for v's target.
func (g *globals) stageVariables(cmd *cobra.Command, v varTarget, req apitypes.SetVariablesRequest, what string) error {
	need := needService
	if v.shared {
		need = needEnvironment
	}
	s, t, err := g.open(v.targetFlags, need)
	if err != nil {
		return err
	}
	where := "as shared variables"
	if v.shared {
		t.Service = ""
	} else {
		req.Service = t.Service
		where = "for " + t.Service
	}
	req.Apply = v.deploy
	return g.stage(cmd.Context(), s, t, stageOpts{what: what + " " + where, deploy: v.deploy, detach: v.detach}, func() (apitypes.ChangeResult, error) {
		return send[apitypes.ChangeResult](cmd.Context(), s, http.MethodPatch, t.envPath()+"/variables", req)
	})
}

func newEnvSetCmd(g *globals) *cobra.Command {
	var v varTarget
	cmd := &cobra.Command{
		Use:   "set KEY=value...",
		Short: "Stage setting variables",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			set := map[string]string{}
			for _, a := range args {
				k, val, ok := strings.Cut(a, "=")
				if !ok || k == "" {
					return usageError(fmt.Sprintf("%q is not KEY=value", a), "Quote values with spaces: `dock env set 'GREETING=hello world'`.")
				}
				set[k] = val
			}
			return g.stageVariables(cmd, v, apitypes.SetVariablesRequest{Set: set}, "Set "+keyList(set))
		},
	}
	v.add(cmd)
	return cmd
}

func newEnvUnsetCmd(g *globals) *cobra.Command {
	var v varTarget
	cmd := &cobra.Command{
		Use:   "unset KEY...",
		Short: "Stage removing variables",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.stageVariables(cmd, v, apitypes.SetVariablesRequest{Unset: args}, "Unset "+strings.Join(args, ", "))
		},
	}
	v.add(cmd)
	return cmd
}

func newEnvImportCmd(g *globals) *cobra.Command {
	var v varTarget
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Stage every variable in a .env file",
		Long: `Stage every KEY=value in a .env file. Blank lines and # comments are
skipped, "export " prefixes are allowed, and values may be single- or
double-quoted (double quotes understand \n, \t, \" and \\). Use - for stdin.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			set, err := readDotenv(args[0])
			if err != nil {
				return err
			}
			if len(set) == 0 {
				return usageError("no variables in "+args[0], "Lines look like KEY=value.")
			}
			return g.stageVariables(cmd, v, apitypes.SetVariablesRequest{Set: set}, fmt.Sprintf("Imported %s from %s", plural(len(set), "variable"), args[0]))
		},
	}
	v.add(cmd)
	return cmd
}

func keyList(m map[string]string) string {
	return strings.Join(slices.Sorted(maps.Keys(m)), ", ")
}

// readDotenv parses a .env file (or stdin for "-").
func readDotenv(path string) (map[string]string, error) {
	f := os.Stdin
	if path != "-" {
		var err error
		if f, err = os.Open(path); err != nil {
			return nil, usageError(err.Error(), "Pass the path of a .env file.")
		}
		defer func() { _ = f.Close() }()
	}
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		k, v, ok, err := parseDotenvLine(sc.Text())
		if err != nil {
			return nil, usageError(fmt.Sprintf("%s:%d: %v", path, n, err), "Lines look like KEY=value.")
		}
		if ok {
			out[k] = v
		}
	}
	return out, sc.Err()
}

// parseDotenvLine parses one line; ok is false for blanks and comments.
func parseDotenvLine(line string) (key, value string, ok bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = strings.TrimPrefix(line, "export ")
	key, value, found := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	if !found || key == "" {
		return "", "", false, fmt.Errorf("expected KEY=value")
	}
	value = strings.TrimSpace(value)
	switch {
	case len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"':
		value, err = strconv.Unquote(value)
		if err != nil {
			return "", "", false, fmt.Errorf("bad double-quoted value for %s", key)
		}
	case len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'':
		value = value[1 : len(value)-1]
	default:
		// Unquoted values end at an inline comment.
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
	}
	return key, value, true, nil
}
