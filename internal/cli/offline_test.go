package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// On a server that can't be reached, `dock status` shows the last known
// state and says so, and `dock deploy` refuses before uploading anything.
func TestServerOffline(t *testing.T) {
	setup(t)
	const env = "/api/v1/projects/shop/environments/staging"
	seen := time.Now() // reported before the server went offline
	web := apitypes.Service{
		Project: "shop", Environment: "staging", Name: "web",
		Status: apitypes.ServiceStatus{State: apitypes.ServiceRunning, ServerOffline: true, Server: "t3node", ServerStatus: apitypes.ServerOffline, UpdatedAt: &seen},
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

// A deploy the server refuses before reading the upload (here: no builder
// may build production) says why without claiming it uploaded: the CLI
// asks first (Expect: 100-continue) and only then says "Uploading".
func TestDeployRefusedBeforeUpload(t *testing.T) {
	setup(t)
	const env = "/api/v1/projects/shop/environments/production"
	web := apitypes.Service{Project: "shop", Environment: "production", Name: "web"}
	asked := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+env+"/services/web", jsonReply(web))
	mux.HandleFunc("POST "+env+"/services/web/releases/source", func(w http.ResponseWriter, r *http.Request) {
		asked = r.Header.Get("Expect") == "100-continue"
		problemReply(http.StatusUnprocessableEntity, apitypes.CodeValidation, "production builds only run on servers that accept production environments")(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(clientconfig.EnvURL, srv.URL)
	t.Setenv(clientconfig.EnvToken, "dock_test")

	appDir(t)
	code, _, stderr := exec(t, "deploy", "-p", "shop", "-e", "production", "-s", "web", "--detach")
	if code == 0 || !asked || strings.Contains(stderr, "Uploading") || !strings.Contains(stderr, "production builds only run") {
		t.Errorf("deploy: exit %d, asked first %v\n%s", code, asked, stderr)
	}
}
