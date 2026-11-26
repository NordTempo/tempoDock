package docker

import (
	"testing"
	"time"

	"github.com/moby/moby/api/types/image"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Other versions of the platform's own images go; the pinned version (even
// one a release also used and none keeps), an
// image of another repository and a pin without a digest (which can't
// tell versions apart) stay. Names compare as Docker writes them, with or
// without docker.io/library.
func TestImageRulesReplaced(t *testing.T) {
	const pin = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const other = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	r := &Runtime{}
	ru := r.imageRules(node.PruneImagesRequest{Pinned: []string{"moby/buildkit:v0.34.1@" + pin, "registry:3@" + pin, "restic/restic:latest"}})
	old := time.Now().Add(-2 * time.Hour).Unix()
	for _, tc := range []struct {
		name string
		img  image.Summary
		want bool
	}{
		{"pinned version", image.Summary{RepoDigests: []string{"moby/buildkit@" + pin}, RepoTags: []string{"moby/buildkit:v0.34.1"}}, false},
		{"older version", image.Summary{RepoDigests: []string{"moby/buildkit@" + other}, RepoTags: []string{"moby/buildkit:v0.33.0"}}, true},
		{"older official image, long name", image.Summary{RepoDigests: []string{"docker.io/library/registry@" + other}}, true},
		{"pinned official image, long name", image.Summary{RepoDigests: []string{"docker.io/library/registry@" + pin}}, false},
		{"pin without a digest", image.Summary{RepoDigests: []string{"restic/restic@" + other}}, false},
		{"another repository", image.Summary{RepoDigests: []string{"postgres@" + other}}, false},
		{"no names", image.Summary{RepoTags: []string{"<none>:<none>"}}, false},
	} {
		tc.img.ID, tc.img.Created = "sha256:x", old
		if got := ru.removable(tc.img); got != tc.want {
			t.Errorf("%s: removable = %v, want %v", tc.name, got, tc.want)
		}
	}
	// The version in use stays even when a release used it and none keeps it.
	shared := image.Summary{ID: "sha256:z", Created: old, RepoDigests: []string{"moby/buildkit@" + pin}}
	if ru := r.imageRules(node.PruneImagesRequest{Pinned: []string{"moby/buildkit@" + pin}, Unneeded: []string{shared.ID}}); ru.removable(shared) {
		t.Error("the pinned version a release also used is removable")
	}
	// A protected name keeps its image, whatever says it may go.
	kept := image.Summary{ID: "sha256:k", Created: old, RepoTags: []string{"alpine:latest"}}
	if ru := r.imageRules(node.PruneImagesRequest{Protect: []string{"docker.io/library/alpine:latest"}, Unneeded: []string{kept.ID}}); ru.removable(kept) {
		t.Error("a protected image is removable")
	}
	// Protected by tag and digest: Docker names it by the digest alone.
	both := image.Summary{ID: "sha256:b", Created: old, RepoTags: []string{"moby/buildkit:v0.34.1"}, RepoDigests: []string{"moby/buildkit@" + other}}
	if ru := r.imageRules(node.PruneImagesRequest{Protect: []string{"moby/buildkit:v0.34.1@" + other}, Unneeded: []string{both.ID}}); ru.removable(both) {
		t.Error("an image protected by tag and digest is removable")
	}
	young := image.Summary{ID: "sha256:y", Created: time.Now().Unix(), RepoDigests: []string{"moby/buildkit@" + other}}
	if ru.removable(young) {
		t.Error("an image made just now is removable")
	}
}

// A rollbacks-only prune takes the rollbacks it is given and nothing
// else, but never the pinned version in use nor a protected image.
func TestImageRulesRollbacksOnly(t *testing.T) {
	const pin = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	r := &Runtime{}
	old := time.Now().Add(-2 * time.Hour).Unix()
	rollback := image.Summary{ID: "sha256:r", Created: old, Labels: map[string]string{node.LabelImage: "true"}}
	pinned := image.Summary{ID: "sha256:p", Created: old, RepoDigests: []string{"restic/restic@" + pin}}
	kept := image.Summary{ID: "sha256:k", Created: old, RepoTags: []string{"alpine:latest"}}
	other := image.Summary{ID: "sha256:o", Created: old, Labels: map[string]string{node.LabelImage: "true"}}
	ru := r.imageRules(node.PruneImagesRequest{
		Rollbacks: []string{rollback.ID, pinned.ID, kept.ID}, OnlyRollbacks: true,
		Pinned: []string{"restic/restic@" + pin}, Protect: []string{"alpine:latest"},
	})
	for _, tc := range []struct {
		name string
		img  image.Summary
		want bool
	}{{"rollback", rollback, true}, {"pinned version", pinned, false}, {"protected", kept, false}, {"not a rollback", other, false}} {
		if got := ru.prunes(tc.img); got != tc.want {
			t.Errorf("%s: prunes = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Only the names the platform gives are its own: a build tag and that
// repository under its registry, not another repository that only
// starts the same.
func TestPlatformName(t *testing.T) {
	repos := []string{"127.0.0.1:5000/dock/p"}
	for name, want := range map[string]bool{
		"dock/p1_e2_s3:12":                      true,
		"127.0.0.1:5000/dock/p1_e2_s3@sha256:a": true,
		"dock/postgres:keep":                    false,
		"127.0.0.1:5000/dock/postgres:1":        false,
		"example.com/dock/p1_e2_s3:12":          false,
		"theirs/dock/p1_e2_s3:12":               false,
	} {
		if got := platformName(name, repos); got != want {
			t.Errorf("platformName(%q) = %v, want %v", name, got, want)
		}
	}
}
