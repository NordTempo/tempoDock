package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Mode is how the agent reaches Dock: the MCP endpoint, or the CLI with
// --json through a Bash tool that only runs `dock`.
type Mode string

const (
	ModeMCP Mode = "mcp"
	ModeCLI Mode = "cli"
)

// system is appended to Claude Code's system prompt in every case: the
// run is headless, so questions can't be answered.
const system = `You operate Tempo Dock, a deploy platform, on behalf of a developer. Nobody can answer questions during this task: do it, or explain why you can't. End with a short summary of what you did or found.`

// cliSystem tells a CLI-mode agent where Dock is; the MCP mode needs
// nothing, the endpoint describes itself.
const cliSystem = ` Use the dock CLI through Bash (DOCK_URL and DOCK_TOKEN are set; pass --json). Only dock commands run; write files with the Write tool.`

// Transcript is what one agent run did, from Claude Code's stream-json.
type Transcript struct {
	Calls  []ToolCall `json:"calls"`
	Answer string     `json:"answer"` // the final text
	Turns  int        `json:"turns"`
	// TokensIn counts every input token over all turns (uncached, cache
	// writes and cache reads): what the context really cost.
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
	Error     string  `json:"error,omitempty"` // the run itself failed
	Duration  float64 `json:"duration_s"`
}

// ToolCall is one tool use and its result.
type ToolCall struct {
	Name    string          `json:"name"` // without the mcp__dock__ prefix
	Input   json.RawMessage `json:"input"`
	Result  string          `json:"result"`
	IsError bool            `json:"is_error,omitempty"`
}

// agentRun is one agent invocation.
type agentRun struct {
	Mode   Mode
	Model  string
	Prompt string
	Token  string // the case's scoped token
	D      *daemon
	Log    string // where the raw stream goes
}

// run starts `claude -p` in an empty directory with no user settings,
// CLAUDE.md or other MCP servers, so only the case's prompt and Dock's
// tools are in context.
func (a agentRun) run(ctx context.Context) Transcript {
	start := time.Now()
	work, err := os.MkdirTemp("", "dock-agenteval-work-")
	if err != nil {
		return Transcript{Error: err.Error()}
	}
	defer func() { _ = os.RemoveAll(work) }()
	args := []string{"-p", a.Prompt, "--model", a.Model,
		"--setting-sources", "", "--strict-mcp-config", "--no-session-persistence",
		"--output-format", "stream-json", "--verbose", "--max-turns", "40"}
	env := append(cleanEnv(), "ENABLE_TOOL_SEARCH=false")
	switch a.Mode {
	case ModeMCP:
		cfg := map[string]any{"mcpServers": map[string]any{"dock": map[string]any{
			"type": "http", "url": a.D.url + "/api/v1/mcp",
			"headers": map[string]string{"Authorization": "Bearer " + a.Token},
		}}}
		b, _ := json.Marshal(cfg)
		p := filepath.Join(work, ".mcp-dock.json")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return Transcript{Error: err.Error()}
		}
		args = append(args, "--tools", "", "--mcp-config", p, "--allowedTools", "mcp__dock", "--append-system-prompt", system)
	case ModeCLI:
		// Read and Write too, for files commands take (dock db apply -f).
		args = append(args, "--tools", "Bash,Read,Write", "--allowedTools", "Bash(dock:*)", "Read", "Write", "--append-system-prompt", system+cliSystem)
		env = append(env, "DOCK_URL="+a.D.url, "DOCK_TOKEN="+a.Token,
			"PATH="+filepath.Dir(a.D.bin)+":"+os.Getenv("PATH"), "HOME="+os.Getenv("HOME"))
	default: // no tools: the baseline toolListCost compares with
		args = append(args, "--tools", "")
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir, cmd.Env, cmd.Stdin = work, env, nil
	// The stream goes to the log as it comes, to follow a long case.
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if a.Log != "" {
		if f, err := os.Create(a.Log); err == nil {
			defer func() { _ = f.Close() }()
			cmd.Stdout = io.MultiWriter(&stdout, f)
		}
	}
	err = cmd.Run()
	t := parseStream(stdout.Bytes())
	t.Duration = time.Since(start).Seconds()
	if err != nil && t.Error == "" {
		t.Error = fmt.Sprintf("claude: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return t
}

// streamEvent is the part of a stream-json line the eval reads.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		} `json:"content"`
	} `json:"message"`
	// The result line.
	Result  string  `json:"result"`
	NumTurn int     `json:"num_turns"`
	Cost    float64 `json:"total_cost_usd"`
	IsError bool    `json:"is_error"`
	Usage   struct {
		Input       int `json:"input_tokens"`
		CacheCreate int `json:"cache_creation_input_tokens"`
		CacheRead   int `json:"cache_read_input_tokens"`
		Output      int `json:"output_tokens"`
	} `json:"usage"`
	MCPServers []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
}

// parseStream reads Claude Code's stream-json into a Transcript.
func parseStream(out []byte) Transcript {
	var t Transcript
	byID := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e streamEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "system":
			for _, s := range e.MCPServers {
				if s.Status != "connected" {
					t.Error = fmt.Sprintf("MCP server %s: %s", s.Name, s.Status)
				}
			}
		case "assistant":
			for _, c := range e.Message.Content {
				switch c.Type {
				case "tool_use":
					byID[c.ID] = len(t.Calls)
					t.Calls = append(t.Calls, ToolCall{Name: strings.TrimPrefix(c.Name, "mcp__dock__"), Input: c.Input})
				case "text":
					t.Answer = c.Text
				}
			}
		case "user":
			for _, c := range e.Message.Content {
				if i, ok := byID[c.ToolUseID]; ok && c.Type == "tool_result" {
					t.Calls[i].Result = resultText(c.Content)
					t.Calls[i].IsError = c.IsError
				}
			}
		case "result":
			if e.Result != "" {
				t.Answer = e.Result
			}
			t.Turns, t.CostUSD = e.NumTurn, e.Cost
			t.TokensIn = e.Usage.Input + e.Usage.CacheCreate + e.Usage.CacheRead
			t.TokensOut = e.Usage.Output
			if e.IsError && t.Error == "" {
				t.Error = "run ended with " + e.Subtype
			}
		}
	}
	return t
}

// resultText flattens a tool result's content: a string, or text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	parts := make([]string, len(blocks))
	for i, b := range blocks {
		parts[i] = b.Text
	}
	return strings.Join(parts, "\n")
}

// toolListCost measures what Dock's tool list costs in context: the input
// tokens of a one-word prompt with the MCP server minus without it. It
// includes the server's instructions, which are sent alongside.
func toolListCost(ctx context.Context, d *daemon, model, token string) (int, error) {
	in := func(mcp bool) (int, error) {
		a := agentRun{Mode: ModeMCP, Model: model, Prompt: "Reply OK", Token: token, D: d}
		if !mcp {
			a.Mode = "none"
		}
		t := a.run(ctx)
		if t.Error != "" {
			return 0, fmt.Errorf("%s", t.Error)
		}
		return t.TokensIn, nil
	}
	with, err := in(true)
	if err != nil {
		return 0, err
	}
	without, err := in(false)
	if err != nil {
		return 0, err
	}
	return with - without, nil
}
