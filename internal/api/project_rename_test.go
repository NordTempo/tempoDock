package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Rename keeps the project's ID, which finds it again; project-path
// requests reject name reuse when a linked client supplies its original
// project ID.
func TestProjectRename(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p, _ := f.core.GetProject(ctx, sys, "shop")
	r, b := f.do("admin", "PATCH", projectsPath+"/shop", apitypes.RenameProjectRequest{ID: p.ID, Name: "storefront"})
	var got apitypes.Project
	if err := json.Unmarshal(b, &got); err != nil || r.StatusCode != http.StatusOK || got.ID != p.ID || got.Name != "storefront" {
		t.Fatalf("rename: %+v %d %s %v", got, r.StatusCode, b, err)
	}
	r, b = f.do("read", "GET", Prefix+"/project-ids/"+strconv.FormatInt(p.ID, 10), nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("by ID: %d %s", r.StatusCode, b)
	}
	if _, err := f.core.CreateProject(ctx, sys, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{projectsPath + "/shop", projectsPath + "/shop/environments/production"} {
		req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+f.tokens["admin"])
		req.Header.Set("Dock-Project-ID", strconv.FormatInt(p.ID, 10))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("name reuse at %s: %d %s", path, resp.StatusCode, body)
		}
	}
}
