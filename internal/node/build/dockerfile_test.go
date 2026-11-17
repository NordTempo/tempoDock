package build

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	opt, _, err := dockerfileOpt(t.TempDir(), src, "Dockerfile", vars, "p1_e1", io.Discard)
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

// TestDockerfileOptKeysSecretSteps checks that a Dockerfile mounting a
// secret builds from a keyed copy, next to its own ignore file, with the
// key as a build arg the source can't override.
func TestDockerfileOptKeysSecretSteps(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	df := "FROM alpine\nARG DOCK_CACHE_KEY\nRUN --mount=type=secret,id=TOKEN x\n"
	for name, body := range map[string]string{"Dockerfile": df, "Dockerfile.dockerignore": "node_modules\n"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]string{"TOKEN": "t", "DOCK_CACHE_KEY": "chosen"}
	opt, _, err := dockerfileOpt(dir, src, "Dockerfile", vars, "p1_e1", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := opt.FrontendAttrs["build-arg:"+cacheKeyArg], secretsKey("p1_e1", vars, []string{"TOKEN"}); got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
	keyed, err := os.ReadFile(filepath.Join(dir, "dockerfile", "Dockerfile"))
	if err != nil || string(keyed) != "FROM alpine\nARG DOCK_CACHE_KEY\nARG DOCK_CACHE_KEY\nRUN --mount=type=secret,id=TOKEN x\n" {
		t.Errorf("keyed Dockerfile = %q, %v", keyed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dockerfile", "Dockerfile.dockerignore")); err != nil {
		t.Errorf("ignore file not copied: %v", err)
	}
}

// TestKeySecretSteps checks where the cache key ARG goes (before the
// first secret-mounting RUN of each stage) and which secrets it covers.
func TestKeySecretSteps(t *testing.T) {
	for _, tc := range []struct {
		name, df, want string
		ids            []string // nil: every variable
	}{{
		name: "no secrets",
		df:   "FROM alpine\nRUN --mount=type=cache,target=/c echo\n",
	}, {
		name: "first secret step of each stage",
		df: "FROM alpine AS a\nRUN echo\nRUN --mount=type=secret,id=NPM_TOKEN \\\n  --mount=type=secret,target=/run/secrets/PIP mk\n" +
			"RUN --mount=type=secret,src=OTHER x\nFROM a\nRUN --mount=type=bind,target=/x y\nRUN --mount=type=secret,id=NPM_TOKEN,env=T z\n",
		want: "FROM alpine AS a\nRUN echo\nARG DOCK_CACHE_KEY\nRUN --mount=type=secret,id=NPM_TOKEN \\\n  --mount=type=secret,target=/run/secrets/PIP mk\n" +
			"RUN --mount=type=secret,src=OTHER x\nFROM a\nRUN --mount=type=bind,target=/x y\nARG DOCK_CACHE_KEY\nRUN --mount=type=secret,id=NPM_TOKEN,env=T z\n",
		ids: []string{"NPM_TOKEN", "OTHER", "PIP"},
	}, {
		name: "id from a variable",
		df:   "FROM alpine\nARG WHICH\nRUN --mount=type=secret,id=$WHICH x\n",
		want: "FROM alpine\nARG WHICH\nARG DOCK_CACHE_KEY\nRUN --mount=type=secret,id=$WHICH x\n",
	}, {
		name: "type from a variable",
		df:   "FROM alpine\nRUN --mount=type=${T},id=X x\n",
		want: "FROM alpine\nARG DOCK_CACHE_KEY\nRUN --mount=type=${T},id=X x\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			keyed, ids, err := keySecretSteps([]byte(tc.df))
			if err != nil {
				t.Fatal(err)
			}
			if string(keyed) != tc.want {
				t.Errorf("keyed =\n%s\nwant\n%s", keyed, tc.want)
			}
			if !slices.Equal(ids, tc.ids) {
				t.Errorf("ids = %q, want %q", ids, tc.ids)
			}
		})
	}
}

// TestSecretsKey checks that the key follows the scope and the named
// secrets' values, and nothing else.
func TestSecretsKey(t *testing.T) {
	vars := map[string]string{"TOKEN": "a", "OTHER": "b"}
	base := secretsKey("p1_e1", vars, []string{"TOKEN"})
	if secretsKey("p1_e1", map[string]string{"TOKEN": "a", "OTHER": "changed"}, []string{"TOKEN"}) != base {
		t.Error("a variable no step mounts changed the key")
	}
	for name, k := range map[string]string{
		"another scope":  secretsKey("p1_e2", vars, []string{"TOKEN"}),
		"another value":  secretsKey("p1_e1", map[string]string{"TOKEN": "c"}, []string{"TOKEN"}),
		"value unset":    secretsKey("p1_e1", map[string]string{}, []string{"TOKEN"}),
		"name and value": secretsKey("p1_e1", map[string]string{"TOKE": "Na"}, []string{"TOKE"}),
	} {
		if k == base {
			t.Errorf("%s gave the same key", name)
		}
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

func TestNextCacheHint(t *testing.T) {
	const next = `{"dependencies": {"next": "15.5.6", "react": "19.1.1"}}`
	example := `FROM node:22-alpine AS base
FROM base AS deps
WORKDIR /app
RUN pnpm i --frozen-lockfile
FROM base AS builder
WORKDIR /srv
WORKDIR web
RUN --mount=type=cache,target=/root/.npm \
  if [ -f pnpm-lock.yaml ]; then pnpm run build; fi
FROM builder AS runner
RUN npm run build
`
	cases := map[string]struct {
		pkg, dockerfile, target string // target "" means no hint
	}{
		"example":       {next, example, "/srv/web/.next/cache"},
		"inherited dir": {next, "FROM node AS b\nWORKDIR /x\nFROM b\nRUN next build\n", "/x/.next/cache"},
		"variable dir":  {next, "FROM node\nWORKDIR $DIR\nRUN yarn build\n", "/app/.next/cache"},
		"has the mount": {next, "FROM node\nWORKDIR /app\nRUN --mount=type=cache,target=/app/.next/cache npm run build\n", ""},
		"no build step": {next, "FROM node\nCOPY . .\nCMD [\"next\", \"start\"]\n", ""},
		"not next":      {`{"dependencies": {"vite": "7"}}`, "FROM node\nRUN npm run build\n", ""},
		"next in dev":   {`{"devDependencies": {"next": "15"}}`, "FROM node\nRUN npm run build\n", "/.next/cache"},
		"no package":    {"", "FROM node\nRUN npm run build\n", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if c.pkg != "" {
				if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(c.pkg), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			hint := nextCacheHint([]byte(c.dockerfile), root)
			if c.target == "" {
				if hint != "" {
					t.Fatalf("unexpected hint: %s", hint)
				}
				return
			}
			if !strings.Contains(hint, "target="+c.target+" ") {
				t.Fatalf("hint = %q, want one naming %s", hint, c.target)
			}
		})
	}
}
