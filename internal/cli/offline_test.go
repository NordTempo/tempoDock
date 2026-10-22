package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// On a server that can't be reached, `dock status` shows the last known
// state and says so, and `dock deploy` refuses before uploading anything.
func TestServerOffline(t *testing.T) {
	setup(t)
	const env = "/api/v1/projects/shop/environments/staging"
	web := apitypes.Service{
		Project: "shop", Environment: "staging", Name: "web",
		Status: apitypes.ServiceStatus{State: apitypes.ServiceRunning, ServerOffline: true, Server: "t3node", ServerStatus: apitypes.ServerOffline},
	}
	overview := apitypes.ProjectOverview{Environments: []apitypes.EnvironmentOverview{{
		Environment: apitypes.Environment{Project: "shop", Name: "staging", Server: "t3node"},
		Services:    []apitypes.ServiceOverview{{Service: web}},
	}}}
	uploaded := false
	newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /api/v1/projects/shop/overview": jsonReply(overview),
		"GET " + env + "/services/web":       jsonReply(web),
		"POST " + env + "/services/web/releases/source": func(w http.ResponseWriter, _ *http.Request) {
			uploaded = true
			w.WriteHeader(http.StatusServiceUnavailable)
		},
	})
	target := []string{"-p", "shop", "-e", "staging", "-s", "web"}

	code, stdout, stderr := exec(t, append([]string{"status"}, target...)...)
	if code != 0 || !strings.Contains(stdout, "server t3node is offline: last known running") {
		t.Errorf("status: exit %d\n%s%s", code, stdout, stderr)
	}
	code, stdout, _ = exec(t, "status", "-p", "shop", "-e", "staging", "--all")
	if code != 0 || !strings.Contains(stdout, "server t3node is offline") || !strings.Contains(stdout, "running (last known)") {
		t.Errorf("status --all: exit %d\n%s", code, stdout)
	}

	appDir(t)
	code, _, stderr = exec(t, append([]string{"deploy"}, target...)...)
	if code != int(output.ExitUnreachable) || !strings.Contains(stderr, "server t3node is offline") || uploaded {
		t.Errorf("deploy: exit %d, uploaded %v\n%s", code, uploaded, stderr)
	}
}
