// Command agenteval measures how well AI agents use Tempo Dock: realistic
// tasks run by a real model (Claude Code headless, `claude -p`) against a
// fresh fake-runtime daemon per case, through the MCP endpoint or the CLI
// with --json, each checked programmatically against the daemon's end
// state and the agent's answer. It reports pass rate, tool calls, tokens
// per task, and what the MCP tool list itself costs in context.
//
//	make agent-eval                                  # every case, MCP, Sonnet
//	go run ./scripts/agenteval -cases 'rollback|diagnose' -mode cli
//	go run ./scripts/agenteval -dry                  # setup and checks only, no model
//
// See scripts/agenteval/README.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Case is one agent task: a world to set up, a prompt, the token the agent
// gets, and a check of what it did.
type Case struct {
	Name   string
	Prompt string
	// Token is the agent's token; the zero value is deploy on shop/staging.
	Token TokenSpec
	// Env adds environment to the daemon (DOCK_FAKE_* hooks). Values may
	// use {dir}, the case's scratch directory, for hook files Setup writes.
	Env map[string]string
	// Needs names the database the case's daemon dials (DOCK_FAKE_DIAL):
	// postgres or redis, a container of the case's own.
	Needs string
	Setup func(*World) error
	// Admin, when set, plays the person in the UI while the agent runs:
	// approve or reject its access requests (Decide).
	Admin *Decision
	// Check returns why the case failed, nil when it passed.
	Check func(*World, Transcript) error
}

// TokenSpec is the agent token's scope and limits: by default limited to
// shop/staging.
type TokenSpec struct {
	Scope                apitypes.Scope
	Project, Environment string
	// AllEnvs limits the token to the project only; Unlimited to nothing.
	AllEnvs, Unlimited bool
	// Data are `dock token create` flags for data access, e.g.
	// --data-non-production write.
	Data []string
}

// spec fills TokenSpec's defaults.
func (s TokenSpec) spec() TokenSpec {
	if s.Scope == "" {
		s.Scope = apitypes.ScopeDeploy
	}
	if s.Unlimited {
		return s
	}
	if s.Project == "" {
		s.Project = "shop"
	}
	if !s.AllEnvs && s.Environment == "" {
		s.Environment = "staging"
	}
	return s
}

// Result is one case run.
type Result struct {
	Case   string `json:"case"`
	Mode   Mode   `json:"mode"`
	Model  string `json:"model"`
	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
	Transcript
}

func main() {
	var (
		pattern  = flag.String("cases", "", "run cases whose name matches this regexp")
		mode     = flag.String("mode", "mcp", "mcp, cli or both")
		model    = flag.String("model", "claude-sonnet-5-5", "the model the agent runs on")
		parallel = flag.Int("parallel", 4, "cases at once (each one daemon and one claude)")
		runs     = flag.Int("runs", 1, "runs of each case")
		dockBin  = flag.String("dock", "", "the dock binary (default: build one)")
		outDir   = flag.String("out", "", "results directory (default .data/agenteval/<time>)")
		dry      = flag.Bool("dry", false, "set up and check each case without an agent; every check should fail")
		list     = flag.Bool("list", false, "list the cases and exit")
		noCost   = flag.Bool("no-cost", false, "skip measuring the tool list's token cost")
		tools    = flag.Bool("tools", false, "print each MCP tool's description and schema size, largest first, and exit")
	)
	flag.Parse()
	re := regexp.MustCompile(*pattern)
	cases := slices.DeleteFunc(allCases(), func(c Case) bool { return !re.MatchString(c.Name) })
	if *list {
		for _, c := range cases {
			fmt.Println(c.Name)
		}
		return
	}
	if err := checkNames(allCases()); err != nil {
		fatal(err)
	}
	modes := []Mode{Mode(*mode)}
	if *mode == "both" {
		modes = []Mode{ModeMCP, ModeCLI}
	}
	bin := *dockBin
	if bin == "" {
		var err error
		if bin, err = buildDock(); err != nil {
			fatal(err)
		}
	}
	if *tools {
		if err := printTools(bin); err != nil {
			fatal(err)
		}
		return
	}
	out := *outDir
	if out == "" {
		out = filepath.Join(".data", "agenteval", time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fatal(err)
	}
	ctx := context.Background()
	summary := report{Model: *model, Out: out}
	if !*dry && !*noCost && slices.Contains(modes, ModeMCP) {
		bytes, tokens, err := toolListSize(ctx, bin, *model)
		if err != nil {
			fatal(fmt.Errorf("tool list: %w", err))
		}
		summary.ToolListBytes, summary.ToolListTokens = bytes, tokens
		fmt.Fprintf(os.Stderr, "tool list: %d bytes, %d tokens\n", bytes, tokens)
	}

	type job struct {
		c   Case
		m   Mode
		run int
	}
	var jobs []job
	for _, m := range modes {
		for _, c := range cases {
			for r := range *runs {
				jobs = append(jobs, job{c, m, r})
			}
		}
	}
	results := make([]Result, len(jobs))
	sem := make(chan struct{}, max(1, *parallel))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			log := filepath.Join(out, fmt.Sprintf("%s-%s-%d.jsonl", j.c.Name, j.m, j.run))
			results[i] = runCase(ctx, j.c, j.m, *model, bin, log, *dry)
			r := results[i]
			status := "PASS"
			if !r.Pass {
				status = "FAIL"
			}
			fmt.Fprintf(os.Stderr, "%s %-34s %s calls=%-2d in=%-7d out=%-5d %.0fs %s\n", status, r.Case, r.Mode, len(r.Calls), r.TokensIn, r.TokensOut, r.Duration, r.Reason)
		})
	}
	wg.Wait()
	summary.Results = results
	if err := summary.write(); err != nil {
		fatal(err)
	}
	summary.print()
	if *dry {
		// A check that passes without an agent checks nothing.
		var vacuous []string
		for _, r := range results {
			if r.Pass {
				vacuous = append(vacuous, r.Case)
			}
		}
		if len(vacuous) > 0 {
			fatal(fmt.Errorf("checks pass without an agent: %s", strings.Join(vacuous, ", ")))
		}
	}
}

// runCase sets a case's world up, runs the agent and checks the result.
func runCase(ctx context.Context, c Case, m Mode, model, bin, log string, dry bool) Result {
	res := Result{Case: c.Name, Mode: m, Model: model}
	fail := func(err error) Result {
		res.Reason = err.Error()
		return res
	}
	scratch, err := os.MkdirTemp("", "dock-agenteval-case-")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = strings.ReplaceAll(v, "{dir}", scratch)
	}
	var db *database
	if c.Needs != "" {
		if db, err = startDatabase(c.Needs); err != nil {
			return fail(fmt.Errorf("start %s: %w", c.Needs, err))
		}
		defer db.stop()
		env["DOCK_FAKE_DIAL"] = db.addr
	}
	d, err := startDaemon(bin, env)
	if err != nil {
		return fail(err)
	}
	defer func() {
		// The daemon's log goes next to the agent's, for failures.
		if b, err := os.ReadFile(filepath.Join(d.dir, "daemon.log")); err == nil {
			_ = os.WriteFile(strings.TrimSuffix(log, ".jsonl")+".daemon.log", b, 0o644)
		}
		d.stop()
	}()
	w := &World{daemon: d, DB: db, Vars: map[string]string{"dir": scratch}}
	if c.Setup != nil {
		if err := c.Setup(w); err != nil {
			return fail(fmt.Errorf("setup: %w", err))
		}
	}
	ts := c.Token.spec()
	token, err := w.token(ts.Scope, ts.Project, ts.Environment, ts.Data...)
	if err != nil {
		return fail(fmt.Errorf("token: %w", err))
	}
	if !dry {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		if c.Admin != nil {
			go c.Admin.decide(cctx, w)
		}
		res.Transcript = agentRun{Mode: m, Model: model, Prompt: c.Prompt, Token: token, D: d, Log: log}.run(cctx)
		cancel()
		if res.Error != "" {
			return fail(fmt.Errorf("agent: %s", res.Error))
		}
	}
	if err := c.Check(w, res.Transcript); err != nil {
		return fail(err)
	}
	res.Pass = true
	return res
}

// toolListSize starts a daemon and measures tools/list for a deploy
// token: its JSON size, and its token cost through the model.
func toolListSize(ctx context.Context, bin, model string) (int, int, error) {
	d, token, err := toolDaemon(bin)
	if err != nil {
		return 0, 0, err
	}
	defer d.stop()
	list, err := toolList(d, token)
	if err != nil {
		return 0, 0, err
	}
	tokens, err := toolListCost(ctx, d, model, token)
	return len(list), tokens, err
}

// toolDaemon starts a daemon with project shop and a deploy token for
// shop/production, the token every tools/list measurement uses.
func toolDaemon(bin string) (*daemon, string, error) {
	d, err := startDaemon(bin, nil)
	if err != nil {
		return nil, "", err
	}
	w := &World{daemon: d}
	if err := w.run("project", "create", "shop"); err != nil {
		d.stop()
		return nil, "", err
	}
	token, err := w.token(apitypes.ScopeDeploy, "shop", "production")
	if err != nil {
		d.stop()
	}
	return d, token, err
}

// printTools prints what each tool costs in tools/list: what the model
// reads of it, its description and its input schema, largest first.
func printTools(bin string) error {
	d, token, err := toolDaemon(bin)
	if err != nil {
		return err
	}
	defer d.stop()
	list, err := toolList(d, token)
	if err != nil {
		return err
	}
	type tool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	var v struct {
		Tools []tool `json:"tools"`
	}
	if err := json.Unmarshal(list, &v); err != nil {
		return err
	}
	slices.SortFunc(v.Tools, func(a, b tool) int {
		return len(b.Description) + len(b.InputSchema) - len(a.Description) - len(a.InputSchema)
	})
	desc, schema := 0, 0
	for _, t := range v.Tools {
		fmt.Printf("%-28s %5d %5d\n", t.Name, len(t.Description), len(t.InputSchema))
		desc += len(t.Description)
		schema += len(t.InputSchema)
	}
	fmt.Printf("%d tools: descriptions %d bytes, schemas %d bytes, tools/list %d bytes\n", len(v.Tools), desc, schema, len(list))
	return nil
}

// toolList is the tools/list result as the endpoint sends it.
func toolList(d *daemon, token string) (json.RawMessage, error) {
	req, _ := http.NewRequest(http.MethodPost, d.url+"/api/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var v struct {
		Result json.RawMessage `json:"result"`
	}
	err = json.NewDecoder(resp.Body).Decode(&v)
	return v.Result, err
}

// buildDock builds the dock binary into a temp dir (no UI needed).
func buildDock() (string, error) {
	dir, err := os.MkdirTemp("", "dock-agenteval-bin-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "dock")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/dock")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return bin, cmd.Run()
}

// checkNames fails on duplicate or unusable case names.
func checkNames(cs []Case) error {
	seen := map[string]bool{}
	ok := regexp.MustCompile(`^[a-z0-9-]+$`)
	for _, c := range cs {
		if seen[c.Name] || !ok.MatchString(c.Name) {
			return fmt.Errorf("case name %q is a duplicate or not kebab-case", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

// report is a run's results and their totals.
type report struct {
	Model          string   `json:"model"`
	Out            string   `json:"-"`
	ToolListBytes  int      `json:"tool_list_bytes,omitempty"`
	ToolListTokens int      `json:"tool_list_tokens,omitempty"`
	Results        []Result `json:"results"`
}

// write saves the results as results.json in the results directory.
func (r report) write() error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.Out, "results.json"), b, 0o644)
}

// print prints the totals per mode, and the failures.
func (r report) print() {
	fmt.Printf("\nmodel %s", r.Model)
	if r.ToolListTokens > 0 {
		fmt.Printf(", MCP tool list %d bytes, %d tokens", r.ToolListBytes, r.ToolListTokens)
	}
	fmt.Printf("\nresults in %s\n\n", r.Out)
	byMode := map[Mode][]Result{}
	for _, res := range r.Results {
		byMode[res.Mode] = append(byMode[res.Mode], res)
	}
	fmt.Println("| mode | pass | tool calls / task | tokens in / task | tokens out / task |")
	fmt.Println("|---|---|---|---|---|")
	for _, m := range []Mode{ModeMCP, ModeCLI} {
		rs := byMode[m]
		if len(rs) == 0 {
			continue
		}
		pass, calls, in, out := 0, 0, 0, 0
		for _, res := range rs {
			if res.Pass {
				pass++
			}
			calls += len(res.Calls)
			in += res.TokensIn
			out += res.TokensOut
		}
		n := len(rs)
		fmt.Printf("| %s | %d/%d | %.1f | %d | %d |\n", m, pass, n, float64(calls)/float64(n), in/n, out/n)
	}
	for _, res := range r.Results {
		if !res.Pass {
			fmt.Printf("FAIL %s (%s): %s\n", res.Case, res.Mode, res.Reason)
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agenteval:", err)
	os.Exit(1)
}
