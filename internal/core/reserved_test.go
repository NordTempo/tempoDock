package core

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// Every page the web UI has under an organization (/<org>/<page>) is a
// name no project may take: the static route would hide the project.
func TestReservedProjectNamesCoverRoutes(t *testing.T) {
	entries, err := os.ReadDir("../../web/src/routes")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		rest, ok := strings.CutPrefix(strings.TrimSuffix(e.Name(), ".tsx"), "_app.$org.")
		if !ok {
			continue
		}
		page, _, _ := strings.Cut(rest, ".")
		page = strings.TrimSuffix(page, "_")
		if strings.HasPrefix(page, "$") || page == "index" {
			continue
		}
		if !slices.Contains(reservedProjectNames, page) {
			t.Errorf("route %s: %q isn't in reservedProjectNames, so a project of that name can't be reached", e.Name(), page)
		}
	}
}
