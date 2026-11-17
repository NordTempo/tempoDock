package build

import (
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
)

// buildkitSeesSecret reports whether BuildKit reads a secret mount in any
// RUN step of the Dockerfile: its parser, its expansion of the flags as
// shell words with env, and its own parseMount. A Dockerfile it refuses
// mounts nothing: that build fails.
func buildkitSeesSecret(t *testing.T, df string, env []string) bool {
	t.Helper()
	res, err := parser.Parse(strings.NewReader(df))
	if err != nil {
		t.Fatal(err)
	}
	stages, _, err := instructions.Parse(res.AST, nil)
	if err != nil {
		return false
	}
	lex := shell.NewLex('\\')
	expand := func(word string) (string, error) {
		out, _, err := lex.ProcessWord(word, shell.EnvsFromSlice(env))
		return out, err
	}
	secret := false
	for _, st := range stages {
		for _, cmd := range st.Commands {
			run, ok := cmd.(*instructions.RunCommand)
			if !ok {
				continue
			}
			if err := run.Expand(expand); err != nil {
				return false
			}
			for _, m := range instructions.GetMounts(run) {
				secret = secret || m.Type == instructions.MountTypeSecret
			}
		}
	}
	return secret
}

// TestSecretMountsFollowBuildKit: every way of writing a mount that
// BuildKit reads as a secret is one Dock keys, whatever quoting, escaping
// or variable it is written with; and the plain mounts of other types
// stay unkeyed, so their layers are still shared.
func TestSecretMountsFollowBuildKit(t *testing.T) {
	env := []string{"T=secret", "M=type=secret,id=X", "K=type"}
	secrets := 0
	for mount, plain := range map[string]bool{
		// Plain: Dock tells exactly.
		`type=secret,id=X`:                false,
		`type=SECRET,id=X`:                false,
		`TYPE=secret,id=X`:                false,
		`id=X,type=secret`:                false,
		`type=cache,target=/c`:            true,
		`type=bind,target=/x`:             true,
		`target=/x`:                       true,
		`type=tmpfs,target=/t`:            true,
		`type=ssh`:                        true,
		`type=cache,target=/c,id=secret`:  true,
		`type=bind,target=/secret,ro`:     true,
		`type=cache,type=secret,id=X`:     false,
		`type=secret,type=cache,target=/`: true,
		// One level of quotes and backslashes: the Dockerfile parser takes
		// it away, so the flag reads plainly.
		`type=s\ecret,id=X`:  false,
		`"type=secret,id=X"`: false,
		`type="secret",id=X`: false,
		`type=sec''ret,id=X`: false,
		// A second level is left in the flag, and BuildKit's expansion of
		// it as a shell word takes that away too.
		`type=s\\ecret,id=X`:   false,
		`type=s\\\ecret,id=X`:  false,
		`type=sec\'\'ret,id=X`: false,
		`type=s"\\e"cret,id=X`: false,
		`type=secret\\,id=X`:   false,
		`type=\"secret\",id=X`: false,
		`ty\\pe=secret,id=X`:   false,
		// Variables.
		`type=$T,id=X`:          false,
		`type=\$T,id=X`:         false,
		`type=${T},id=X`:        false,
		`$M`:                    false,
		`${K}=secret,id=X`:      false,
		`type=secret,id=$T`:     false,
		`type=cache,target=/$T`: false, // keyed without need: a variable is in it
	} {
		df := "FROM alpine\nRUN --mount=" + mount + " true\n"
		res, err := parser.Parse(strings.NewReader(df))
		if err != nil {
			t.Fatal(err)
		}
		found, _ := secretMounts(res.AST.Children[1], map[string]bool{})
		sees := buildkitSeesSecret(t, df, env)
		if sees {
			secrets++
		}
		switch {
		case sees && !found:
			t.Errorf("--mount=%s: BuildKit mounts a secret and the step is not keyed", mount)
		case plain && found:
			t.Errorf("--mount=%s: a plain mount of another type is keyed", mount)
		case plain && sees:
			t.Errorf("--mount=%s: listed as plain, but BuildKit mounts a secret", mount)
		}
	}
	// The escaped, quoted and variable forms must be among them, or the
	// table no longer shows what it is for.
	if secrets < 10 {
		t.Errorf("BuildKit read a secret mount in only %d of the cases", secrets)
	}
}
