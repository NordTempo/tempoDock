package build

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/distribution/reference"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/command"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/buildkit/session"
	"github.com/tonistiigi/go-csvvalue"
)

// maxDockerfileSize bounds how much of an uploaded file is read and parsed
// as the Dockerfile.
const maxDockerfileSize = 1 << 20

// cacheKeyArg is the build arg that keys the layers of RUN steps that
// mount secrets (keySecretSteps).
const cacheKeyArg = "DOCK_CACHE_KEY"

// dockerfileOpt returns options to build src with BuildKit's built-in
// Dockerfile frontend. See the package doc for how vars reach the build.
// dir is the build's own directory, outside src, where a Dockerfile that
// mounts secrets is rewritten to (keySecretSteps).
func dockerfileOpt(dir, src, dockerfile string, vars map[string]string, ns string, logs io.Writer) (bkclient.SolveOpt, string, error) {
	if !filepath.IsLocal(dockerfile) {
		return bkclient.SolveOpt{}, "", fmt.Errorf("dockerfile path %q must be inside the source", dockerfile)
	}
	// extract keeps every symlink inside src, but resolve through os.Root
	// anyway: BuildKit mounts the Dockerfile's directory, so a link out of
	// the tree there would expose host files.
	root, err := os.OpenRoot(src)
	if err != nil {
		return bkclient.SolveOpt{}, "", err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(dockerfile)
	if err != nil || !info.Mode().IsRegular() {
		return bkclient.SolveOpt{}, "", fmt.Errorf("dockerfile %q not found in source", dockerfile)
	}
	if info.Size() > maxDockerfileSize {
		return bkclient.SolveOpt{}, "", fmt.Errorf("dockerfile %q is larger than %d KB", dockerfile, maxDockerfileSize>>10)
	}
	df, err := root.ReadFile(dockerfile)
	if err != nil {
		return bkclient.SolveOpt{}, "", err
	}
	args, err := dockerfileArgs(df, vars)
	if err != nil {
		return bkclient.SolveOpt{}, "", fmt.Errorf("parse %s: %w", dockerfile, err)
	}
	if ref, _, _, ok := parser.DetectSyntax(df); ok {
		_, _ = fmt.Fprintf(logs, "ignoring the syntax directive (%s): builds use BuildKit's built-in Dockerfile frontend\n", ref)
	}
	if hint := nextCacheHint(df, root); hint != "" {
		_, _ = fmt.Fprintln(logs, hint)
	}

	attrs := map[string]string{
		"filename": filepath.Base(dockerfile),
		// The built-in frontend treats a set cmdline as "already forwarded"
		// and ignores `# syntax=` directives: a custom frontend could mount
		// any cache ID and bypass the namespace below.
		"cmdline": "",
		// Prefixes every cache mount ID, including the default (the target
		// path) when the Dockerfile sets none.
		"build-arg:BUILDKIT_CACHE_MOUNT_NS": ns,
	}
	for k, v := range args {
		attrs["build-arg:"+k] = v
	}
	dfDir := filepath.Join(src, filepath.Dir(dockerfile))
	keyed, ids, err := keySecretSteps(df)
	if err != nil {
		return bkclient.SolveOpt{}, "", fmt.Errorf("parse %s: %w", dockerfile, err)
	}
	if keyed != nil {
		if ids == nil {
			ids = slices.Collect(maps.Keys(vars))
		}
		attrs["build-arg:"+cacheKeyArg] = secretsKey(ns, vars, ids)
		if dfDir, err = writeKeyed(dir, root, dockerfile, keyed); err != nil {
			return bkclient.SolveOpt{}, "", err
		}
		_, _ = fmt.Fprintf(logs, "steps that mount secrets are cached per environment: the build adds ARG %s before the first in each stage\n", cacheKeyArg)
	}
	opt, err := localOpt("dockerfile.v0", attrs, src, dfDir)
	opt.Session = []session.Attachable{secretStore(vars)}
	return opt, baseImage(df, vars), err
}

// keySecretSteps returns the Dockerfile with `ARG DOCK_CACHE_KEY` added
// before the first RUN step of each stage that mounts a secret, or nil
// when no step does. BuildKit leaves secret values out of a step's cache
// key, so without it two builds with the same base, files and RUN line
// share the layer whatever their secrets: one environment or org could
// hand another a layer built from its own PIP_INDEX_URL. A declared ARG
// is in the environment of every later RUN in its stage, so its value
// (secretsKey) enters their cache keys; later steps and stages built on
// those layers chain on them. An ENV of the same name earlier in the
// Dockerfile (or the base image) doesn't stand in for it: the ARG comes
// later and wins, and a step after the first keyed one already chains on
// this build's own key. ids are the secrets the steps mount, or nil when
// one can't be told before the build (an id naming a variable): then
// every variable counts.
//
// Line numbers in BuildKit's errors count the added lines.
func keySecretSteps(dockerfile []byte) (keyed []byte, ids []string, err error) {
	res, err := parser.Parse(bytes.NewReader(dockerfile))
	if err != nil {
		return nil, nil, err
	}
	var at []int // 1-based lines to add the ARG before
	seen, all := map[string]bool{}, false
	stageKeyed := false
	for _, n := range res.AST.Children {
		switch {
		case strings.EqualFold(n.Value, command.From):
			stageKeyed = false
		case strings.EqualFold(n.Value, command.Run):
			found, known := secretMounts(n, seen)
			all = all || !known
			if found && !stageKeyed {
				at, stageKeyed = append(at, n.StartLine), true
			}
		}
	}
	if len(at) == 0 {
		return nil, nil, nil
	}
	lines := strings.SplitAfter(string(dockerfile), "\n")
	var b strings.Builder
	b.Grow(len(dockerfile) + len(at)*(len(cacheKeyArg)+6))
	for i, l := range lines {
		if len(at) > 0 && at[0] == i+1 {
			b.WriteString("ARG " + cacheKeyArg + "\n")
			at = at[1:]
		}
		b.WriteString(l)
	}
	if !all {
		ids = slices.Sorted(maps.Keys(seen))
	}
	return []byte(b.String()), ids, nil
}

// secretMounts reports whether a RUN step mounts a secret, adding the
// ids it mounts to ids. known is false when the ids can't be told before
// the build; then every variable counts.
//
// The Dockerfile parser takes one level of quotes and backslashes off a
// flag; BuildKit then expands each value of the mount as a shell word
// (dockerfile2llb's dispatch, instructions' parseMount): variables are
// replaced, and quotes and backslashes still there are taken away, so a
// type written `s\\ecret` in the file is a secret mount too. Rather than
// follow that reading and fall behind it, a mount counts as one of a
// secret unless it is plainly something else: left with none of the
// characters the expansion acts on, and of a type that is bind, cache,
// tmpfs or ssh (or none, which is bind). A step keyed without need only
// loses layer sharing with other environments; one missed shares a
// secret's layer. TestSecretMountsFollowBuildKit holds this against
// BuildKit's own reading.
func secretMounts(run *parser.Node, ids map[string]bool) (found, known bool) {
	known = true
	for _, f := range run.Flags {
		name, val, _ := strings.Cut(strings.TrimPrefix(f, "--"), "=")
		if name != "mount" {
			continue
		}
		if strings.ContainsAny(val, "$\\\"'") {
			found, known = true, false
			continue
		}
		fields, err := csvvalue.Fields(val, nil)
		if err != nil {
			found, known = true, false
			continue
		}
		var typ, id, source, target string
		for _, field := range fields {
			k, v, _ := strings.Cut(field, "=")
			switch strings.ToLower(k) {
			case "type":
				typ = strings.ToLower(v)
			case "id":
				id = v
			case "source", "src":
				source = v
			case "target", "dst", "destination":
				target = v
			}
		}
		switch typ {
		case "", "bind", "cache", "tmpfs", "ssh":
			continue
		case "secret":
		default:
			// Not a type BuildKit knows today: its build fails, and a
			// later BuildKit's new type isn't shared unkeyed.
			found, known = true, false
			continue
		}
		if source != "" {
			id = source
		}
		if id == "" && target != "" {
			id = path.Base(target)
		}
		found = true
		if id == "" {
			known = false
			continue
		}
		ids[id] = true
	}
	return found, known
}

// secretsKey is a build's DOCK_CACHE_KEY (and Railpack's secrets-hash):
// a hash of its cache namespace and the names and values of the vars
// named in ids. The namespace keeps two scopes from sharing a layer even
// with the same values, so one can't learn another's value by guessing
// it and watching for a cache hit. Like any ARG it shows in the image's
// history, so it is a hash, never the values.
func secretsKey(ns string, vars map[string]string, ids []string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:%s\n", len(ns), ns)
	for _, k := range slices.Sorted(slices.Values(ids)) {
		if v, ok := vars[k]; ok {
			_, _ = fmt.Fprintf(h, "%d:%s=%d:%s\n", len(k), k, len(v), v)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeKeyed writes the keyed Dockerfile into its own directory under
// dir, next to a copy of its own ignore file (<name>.dockerignore) if the
// source has one, and returns the directory: the source stays as it was,
// so COPY . sees the Dockerfile the user wrote.
func writeKeyed(dir string, src *os.Root, dockerfile string, keyed []byte) (string, error) {
	out := filepath.Join(dir, "dockerfile")
	if err := os.Mkdir(out, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(dockerfile)
	if err := os.WriteFile(filepath.Join(out, name), keyed, 0o600); err != nil {
		return "", err
	}
	ignore, err := src.ReadFile(dockerfile + ".dockerignore")
	switch {
	case err == nil:
		if err := os.WriteFile(filepath.Join(out, name+".dockerignore"), ignore, 0o600); err != nil {
			return "", err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	return out, nil
}

// dockerfileArgs picks the vars passed to a Dockerfile build as build args:
// those the Dockerfile declares with ARG, in any stage. BUILDKIT_* names are
// never passed, since they control BuildKit itself (frontend, cache
// namespace). Every var is also available as a secret.
func dockerfileArgs(dockerfile []byte, vars map[string]string) (map[string]string, error) {
	res, err := parser.Parse(bytes.NewReader(dockerfile))
	if err != nil {
		return nil, err
	}
	args := map[string]string{}
	for _, n := range res.AST.Children {
		if !strings.EqualFold(n.Value, command.Arg) {
			continue
		}
		for a := n.Next; a != nil; a = a.Next {
			name, _, _ := strings.Cut(a.Value, "=")
			name = strings.Trim(name, `"'`)
			if v, ok := vars[name]; ok && !strings.HasPrefix(name, "BUILDKIT_") {
				args[name] = v
			}
		}
	}
	return args, nil
}

// baseImage is the image a Dockerfile's final stage is built on, the way
// BuildKit names it (docker.io/library/node:22-alpine): the final FROM,
// followed through stages it names, with build args and ARG defaults
// before the first FROM applied. "" for scratch or one it can't tell
// (a FROM whose variables don't resolve). For the base image hint (ADR
// 0074): a newer image under the same tag is what a rebuild picks up.
func baseImage(dockerfile []byte, vars map[string]string) string {
	res, err := parser.Parse(bytes.NewReader(dockerfile))
	if err != nil {
		return ""
	}
	meta := map[string]string{}
	stages := map[string]string{} // stage name -> its FROM, expanded
	last := ""
	lex := shell.NewLex(res.EscapeToken)
	for _, n := range res.AST.Children {
		switch {
		case strings.EqualFold(n.Value, command.Arg) && len(stages) == 0 && last == "":
			for a := n.Next; a != nil; a = a.Next {
				name, def, _ := strings.Cut(a.Value, "=")
				name = strings.Trim(name, `"'`)
				if v, ok := vars[name]; ok {
					meta[name] = v
				} else {
					meta[name] = strings.Trim(def, `"'`)
				}
			}
		case strings.EqualFold(n.Value, command.From) && n.Next != nil:
			from, _, err := lex.ProcessWord(n.Next.Value, envMap(meta))
			if err != nil {
				from = ""
			}
			last = from
			if as := n.Next.Next; as != nil && strings.EqualFold(as.Value, "as") && as.Next != nil {
				stages[strings.ToLower(as.Next.Value)] = from
			}
		}
	}
	for range len(stages) + 1 {
		from, ok := stages[strings.ToLower(last)]
		if !ok {
			break
		}
		last = from
	}
	if last == "" || strings.EqualFold(last, "scratch") || strings.Contains(last, "$") {
		return ""
	}
	named, err := reference.ParseNormalizedNamed(last)
	if err != nil {
		return ""
	}
	return reference.TagNameOnly(named).String()
}

// envMap looks variables up in a map, for the shell lexer.
type envMap map[string]string

func (m envMap) Get(key string) (string, bool) { v, ok := m[key]; return v, ok }

func (m envMap) Keys() []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// nextBuild matches a RUN step that builds a Next.js app: `next build`, or
// a package manager's build script.
var nextBuild = regexp.MustCompile(`\b(next build|(npm|pnpm|yarn|bun)( run)? build)\b`)

// nextCacheHint returns a build log line for a Dockerfile that builds a
// Next.js app (src's package.json depends on next) without a cache mount
// on .next/cache, or "". Without one every build compiles the whole app
// again; with one only what changed (ADR 0089). The mount's target is the
// .next/cache under the building step's WORKDIR, /app/.next/cache when
// that can't be told.
func nextCacheHint(dockerfile []byte, src *os.Root) string {
	f, err := src.Open("package.json")
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var pkg struct{ Dependencies, DevDependencies map[string]json.RawMessage }
	if json.NewDecoder(io.LimitReader(f, maxDockerfileSize)).Decode(&pkg) != nil ||
		pkg.Dependencies["next"] == nil && pkg.DevDependencies["next"] == nil {
		return ""
	}
	res, err := parser.Parse(bytes.NewReader(dockerfile))
	if err != nil {
		return ""
	}
	workdirs := map[string]string{} // stage name -> its WORKDIR at the end
	stage, workdir, target := "", "/", ""
	for _, n := range res.AST.Children {
		switch {
		case strings.EqualFold(n.Value, command.From) && n.Next != nil:
			workdir, stage = cmp.Or(workdirs[strings.ToLower(n.Next.Value)], "/"), ""
			if as := n.Next.Next; as != nil && strings.EqualFold(as.Value, "as") && as.Next != nil {
				stage = strings.ToLower(as.Next.Value)
			}
		case strings.EqualFold(n.Value, command.Workdir) && n.Next != nil:
			if w := n.Next.Value; path.IsAbs(w) {
				workdir = path.Clean(w)
			} else {
				workdir = path.Join(workdir, w)
			}
		case strings.EqualFold(n.Value, command.Run):
			if slices.ContainsFunc(n.Flags, func(f string) bool {
				return strings.HasPrefix(f, "--mount=") && strings.Contains(f, "type=cache") && strings.Contains(f, ".next/cache")
			}) {
				return ""
			}
			if target == "" && nextBuild.MatchString(n.Original) {
				target = path.Join(workdir, ".next/cache")
				if strings.Contains(workdir, "$") {
					target = "/app/.next/cache"
				}
			}
		}
		if stage != "" {
			workdirs[stage] = workdir
		}
	}
	if target == "" {
		return ""
	}
	return "hint: this Dockerfile builds a Next.js app without keeping .next/cache, so every build compiles it from scratch. " +
		"Keep it between builds with a cache mount on the step that builds: RUN --mount=type=cache,target=" + target + " <your build command>"
}

// fromVertex matches the BuildKit progress line of a FROM step, which
// names the image with the digest it resolved:
// "[base 1/2] FROM docker.io/library/alpine:3.20@sha256:d9e8…".
var fromVertex = regexp.MustCompile(`\bFROM (\S+)@(sha256:[0-9a-f]{64})`)

// familiar is a normalized reference as people write it:
// docker.io/library/node:22-alpine is node:22-alpine.
func familiar(ref string) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ref
	}
	return reference.FamiliarString(named)
}
