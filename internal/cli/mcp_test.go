package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

func TestMCPConfig(t *testing.T) {
	setup(t)
	t.Setenv(clientconfig.EnvURL, "https://dock.example.com/")
	t.Setenv(clientconfig.EnvToken, "dock_secret")

	code, stdout, stderr := exec(t, "mcp", "--json")
	if code != 0 {
		t.Fatalf("dock mcp: %d %s", code, stderr)
	}
	var c mcpConfig
	if err := json.Unmarshal([]byte(stdout), &c); err != nil {
		t.Fatalf("not JSON: %q", stdout)
	}
	want := mcpServer{Type: "http", URL: "https://dock.example.com/api/v1/mcp", Headers: map[string]string{"Authorization": "Bearer ${DOCK_TOKEN}"}}
	if got := c.MCPJSON["dock"]; got.URL != want.URL || got.Type != want.Type || got.Headers["Authorization"] != want.Headers["Authorization"] {
		t.Errorf("mcp_json = %+v", c.MCPJSON)
	}
	if strings.Contains(stdout, "dock_secret") {
		t.Error("the token is printed without --show-token")
	}
	if !strings.Contains(c.ClaudeCode, "'Authorization: Bearer ${DOCK_TOKEN}'") {
		t.Errorf("claude command = %s", c.ClaudeCode)
	}

	if _, stdout, _ := exec(t, "mcp", "--show-token"); !strings.Contains(stdout, "Bearer dock_secret") {
		t.Errorf("--show-token = %s", stdout)
	}
}
