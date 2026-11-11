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
	opt, _, err := dockerfileOpt(src, "Dockerfile", vars, "p1_e1", io.Discard)
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

func TestBaseImage(t *testing.T) {
	for _, c := range []struct {
		name, dockerfile string
		vars             map[string]string
		want             string
	}{
		{"plain", "FROM node:22-alpine\nRUN true\n", nil, "docker.io/library/node:22-alpine"},
		{"no tag", "FROM alpine\n", nil, "docker.io/library/alpine:latest"},
		{"final stage", "FROM golang:1.25 AS build\nRUN true\nFROM gcr.io/distroless/static\nCOPY --from=build /a /a\n", nil, "gcr.io/distroless/static:latest"},
		{"through stages", "ARG V=3.20\nFROM alpine:${V} AS base\nRUN true\nFROM base AS app\nFROM app\n", nil, "docker.io/library/alpine:3.20"},
		{"build arg", "ARG V=3.20\nFROM alpine:$V\n", map[string]string{"V": "3.22"}, "docker.io/library/alpine:3.22"},
		{"platform flag", "FROM --platform=$BUILDPLATFORM ghcr.io/acme/base:1 AS x\nFROM x\n", nil, "ghcr.io/acme/base:1"},
		{"digest", "FROM alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc\n", nil, "docker.io/library/alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"},
		{"scratch", "FROM alpine AS a\nFROM scratch\nCOPY --from=a /x /x\n", nil, ""},
		{"unresolved", "FROM $IMAGE\n", nil, ""},
	} {
		if got := baseImage([]byte(c.dockerfile), c.vars); got != c.want {
			t.Errorf("%s: baseImage = %q, want %q", c.name, got, c.want)
		}
	}
	m := fromVertex.FindStringSubmatch("[base 1/2] FROM docker.io/library/alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc")
	if m == nil || m[1] != "docker.io/library/alpine:3.20" || familiar(m[1]) != "alpine:3.20" {
		t.Fatalf("vertex = %v", m)
	}
}
