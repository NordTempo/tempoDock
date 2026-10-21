package build

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/command"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/session"
)

// maxDockerfileSize bounds how much of an uploaded file is read and parsed
// as the Dockerfile.
const maxDockerfileSize = 1 << 20

// dockerfileOpt returns options to build src with BuildKit's built-in
// Dockerfile frontend. See the package doc for how vars reach the build.
func dockerfileOpt(src, dockerfile string, vars map[string]string, ns string, logs io.Writer) (bkclient.SolveOpt, error) {
	if !filepath.IsLocal(dockerfile) {
		return bkclient.SolveOpt{}, fmt.Errorf("dockerfile path %q must be inside the source", dockerfile)
	}
	// extract keeps every symlink inside src, but resolve through os.Root
	// anyway: BuildKit mounts the Dockerfile's directory, so a link out of
	// the tree there would expose host files.
	root, err := os.OpenRoot(src)
	if err != nil {
		return bkclient.SolveOpt{}, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(dockerfile)
	if err != nil || !info.Mode().IsRegular() {
		return bkclient.SolveOpt{}, fmt.Errorf("dockerfile %q not found in source", dockerfile)
	}
	if info.Size() > maxDockerfileSize {
		return bkclient.SolveOpt{}, fmt.Errorf("dockerfile %q is larger than %d KB", dockerfile, maxDockerfileSize>>10)
	}
	df, err := root.ReadFile(dockerfile)
	if err != nil {
		return bkclient.SolveOpt{}, err
	}
	args, err := dockerfileArgs(df, vars)
	if err != nil {
		return bkclient.SolveOpt{}, fmt.Errorf("parse %s: %w", dockerfile, err)
	}
	if ref, _, _, ok := parser.DetectSyntax(df); ok {
		_, _ = fmt.Fprintf(logs, "ignoring the syntax directive (%s): builds use BuildKit's built-in Dockerfile frontend\n", ref)
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
	opt, err := localOpt("dockerfile.v0", attrs, src, filepath.Join(src, filepath.Dir(dockerfile)))
	opt.Session = []session.Attachable{secretStore(vars)}
	return opt, err
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
