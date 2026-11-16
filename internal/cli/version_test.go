package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/version"
)

func TestVersion(t *testing.T) {
	setup(t)

	// No server configured: the CLI's version only, nothing sent.
	code, stdout, stderr := exec(t, "version", "--json")
	var v versionView
	if code != 0 || json.Unmarshal([]byte(stdout), &v) != nil || v != (versionView{CLI: version.Version}) {
		t.Fatalf("no server: %d %q %q", code, stdout, stderr)
	}

	// A server on another release: its version and the install command,
	// from one unauthenticated request.
	requests := 0
	newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /api/v1/version": func(w http.ResponseWriter, r *http.Request) {
			requests++
			if r.Header.Get("Authorization") != "" {
				t.Error("the version request must not send the token")
			}
			jsonReply(apitypes.Version{Version: "v9.1.0", APIVersion: apitypes.APIVersion})(w, r)
		},
	})
	code, stdout, stderr = exec(t, "version", "--json")
	if code != 0 || json.Unmarshal([]byte(stdout), &v) != nil {
		t.Fatalf("with server: %d %q %q", code, stdout, stderr)
	}
	if v.ServerVersion != "v9.1.0" || !strings.Contains(v.Hint, "DOCK_VERSION=v9.1.0") || requests != 1 {
		t.Errorf("with server: %+v after %d requests", v, requests)
	}
}

func TestUpgradeHint(t *testing.T) {
	if h := upgradeHint("v0.3.0-rc.1"); !strings.Contains(h, "/releases/download/v0.3.0-rc.1/install-cli.sh ") {
		t.Errorf("release: %q", h)
	}
	for _, v := range []string{"dev", "v0.3.0-4-gabc1234", "v0.3.0-dirty"} {
		if h := upgradeHint(v); strings.Contains(h, "/releases/download/") {
			t.Errorf("%s is not a release, got %q", v, h)
		}
	}
}
