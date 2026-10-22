package registry

import (
	"reflect"
	"testing"
)

func TestUnreferenced(t *testing.T) {
	manifests := []Manifest{
		{Repo: "p1/web", Digest: "sha256:a", Tags: []string{"staging-1"}},
		{Repo: "p1/web", Digest: "sha256:b", Tags: []string{"staging-2"}},
		{Repo: "p1/web", Digest: "sha256:c", Tags: []string{"staging-3", "production-1"}},
		{Repo: "p1/api", Digest: "sha256:d", Tags: []string{"staging-1"}},
		{Repo: "p2/web", Digest: "sha256:c", Tags: []string{"dev-1"}},
	}
	// Kept by digest and by digest ref; the same digest is kept in every
	// repository that has it.
	keep := []string{"sha256:b", "registry.example.com/p1/web@sha256:c"}
	got := unreferenced(manifests, keep)
	want := []Manifest{manifests[0], manifests[3]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unreferenced = %v, want %v", got, want)
	}
}

func TestNextPage(t *testing.T) {
	for link, want := range map[string]string{
		`</v2/_catalog?last=p1%2Fweb&n=1000>; rel="next"`:                    "/v2/_catalog?last=p1%2Fweb&n=1000",
		`<http://127.0.0.1:5000/v2/p1/web/tags/list?last=b&n=2>; rel="next"`: "/v2/p1/web/tags/list?last=b&n=2",
		"":                               "",
		`</v2/_catalog?n=1>; rel="prev"`: "",
	} {
		if got := nextPage(link); got != want {
			t.Errorf("nextPage(%q) = %q, want %q", link, got, want)
		}
	}
}
