package build

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

func TestDockerfileArgs(t *testing.T) {
	vars := map[string]string{
		"NODE_ENV": "production", "PUBLIC_URL": "https://x", "MULTI_A": "a", "MULTI_B": "b",
		"CONTINUED": "c", "LOWER": "l", "QUOTED": "q", "LATER_STAGE": "s",
		"DATABASE_URL": "secret", "IN_COMMENT": "no", "IN_RUN": "no", "BUILDKIT_SYNTAX": "evil/frontend",
	}
	df := `# syntax=docker/dockerfile:1
# ARG IN_COMMENT
ARG NODE_ENV
FROM node:22-alpine AS build
ARG PUBLIC_URL=http://localhost
ARG MULTI_A MULTI_B=default
ARG \
  CONTINUED
arg LOWER
ARG "QUOTED"=x
ARG BUILDKIT_SYNTAX
RUN echo ARG IN_RUN
FROM build
ARG LATER_STAGE
`
	got, err := dockerfileArgs([]byte(df), vars)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"NODE_ENV": "production", "PUBLIC_URL": "https://x", "MULTI_A": "a", "MULTI_B": "b",
		"CONTINUED": "c", "LOWER": "l", "QUOTED": "q", "LATER_STAGE": "s",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("build args = %v, want %v", got, want)
	}
}

func TestDockerfileOptScopesCaches(t *testing.T) {
	src := t.TempDir()
	df := "FROM alpine\nARG BUILDKIT_CACHE_MOUNT_NS\nARG GREETING\n"
	if err := os.WriteFile(filepath.Join(src, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"GREETING": "hi", "BUILDKIT_CACHE_MOUNT_NS": "p2_e2", "TOKEN": "secret"}
	opt, err := dockerfileOpt(src, "Dockerfile", vars, "p1_e1", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"filename":                          "Dockerfile",
		"cmdline":                           "",
		"build-arg:BUILDKIT_CACHE_MOUNT_NS": "p1_e1",
		"build-arg:GREETING":                "hi",
	}
	if !maps.Equal(opt.FrontendAttrs, want) {
		t.Fatalf("attrs = %v, want %v", opt.FrontendAttrs, want)
	}
}

func TestCacheNamespace(t *testing.T) {
	for scope, want := range map[string]string{"": "unscoped", "p1_e2": "p1_e2"} {
		if got, err := cacheNamespace(scope); err != nil || got != want {
			t.Errorf("cacheNamespace(%q) = %q, %v; want %q", scope, got, err, want)
		}
	}
	// Separators would let one scope's IDs collide with another's.
	for _, scope := range []string{"p1/e2", "p1-e2", "p1 e2", "../x"} {
		if _, err := cacheNamespace(scope); err == nil {
			t.Errorf("cacheNamespace(%q) accepted", scope)
		}
	}
}
