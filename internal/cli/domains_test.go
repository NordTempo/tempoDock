package cli

import (
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Removing a hostname names the service's redirects that still point at
// it, however the hostname was typed.
func TestRedirectsTo(t *testing.T) {
	domains := []apitypes.Domain{
		{Hostname: "example.com"},
		{Hostname: "www.example.com", RedirectTo: "example.com"},
		{Hostname: "old.example.com", RedirectTo: "example.com"},
		{Hostname: "docs.example.com", RedirectTo: "example.dev"},
	}
	if got, want := redirectsTo(domains, "Example.com."), []string{"www.example.com", "old.example.com"}; !slices.Equal(got, want) {
		t.Errorf("redirectsTo = %v, want %v", got, want)
	}
	if got := redirectsTo(domains, "www.example.com"); len(got) != 0 {
		t.Errorf("nothing redirects to www.example.com, got %v", got)
	}
}
