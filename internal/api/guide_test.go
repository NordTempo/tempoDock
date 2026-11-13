package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The help's guide reader gets the embedded pages, with no sign-in, and
// nothing outside them.
func TestGuidePage(t *testing.T) {
	f := newFixture(t)
	res, b := f.do("", http.MethodGet, "/api/v1/guide/deploying", nil)
	var page apitypes.GuidePage
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &page) != nil || !strings.HasPrefix(page.Markdown, "# Deploying") {
		t.Fatalf("deploying: %d %.120s", res.StatusCode, b)
	}
	for _, path := range []string{"/api/v1/guide/nope", "/api/v1/guide/..%2Fcli"} {
		if res, _ := f.do("", http.MethodGet, path, nil); res.StatusCode < 400 || res.StatusCode >= 500 {
			t.Errorf("%s: %d, want a client error", path, res.StatusCode)
		}
	}
}
