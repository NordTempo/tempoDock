package pack

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// decision is what the walker does with one path.
type decision int

const (
	include decision = iota
	// skip leaves the path out. For a directory, its contents are skipped too.
	skip
	// descend leaves a directory's own entry out but still visits its
	// contents, because a negation pattern may re-include some of them.
	descend
)

// filter decides which paths go into the archive. rel is slash-separated and
// relative to the root.
type filter interface {
	decide(rel string, isDir bool) (decision, error)
	// enter is called for every included directory before its contents are
	// visited, so nested ignore files can be loaded.
	enter(root, rel string) error
}

// dockerignore applies a root .dockerignore with Docker's own matcher, so the
// build sees exactly what `docker build` would.
type dockerignore struct {
	pm *patternmatcher.PatternMatcher
}

func newDockerignore(file string, keep []string) (*dockerignore, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	patterns, err := ignorefile.ReadAll(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	// Like the Docker CLI: files the build needs are sent even if ignored.
	for _, k := range keep {
		patterns = append(patterns, "!"+k)
	}
	pm, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, err
	}
	return &dockerignore{pm: pm}, nil
}

func (d *dockerignore) decide(rel string, isDir bool) (decision, error) {
	matched, err := d.pm.MatchesOrParentMatches(rel)
	if err != nil || !matched {
		return include, err
	}
	if !isDir {
		return skip, nil
	}
	// Only walk into an ignored directory if an exclusion pattern could
	// re-include something inside it (same shortcut as the Docker CLI).
	for _, p := range d.pm.Patterns() {
		if p.Exclusion() && strings.HasPrefix(filepath.ToSlash(p.String())+"/", rel+"/") {
			return descend, nil
		}
	}
	return skip, nil
}

func (d *dockerignore) enter(string, string) error { return nil }

// gitignore applies .gitignore files at the root and in every directory,
// following git's rules: deeper files take precedence, the last matching
// pattern wins, and nothing inside an ignored directory can be re-included.
type gitignore struct {
	keep  []string
	rules []rule
}

// rule is one .gitignore line.
type rule struct {
	base     string   // directory of the .gitignore, relative to root ("" for root)
	segs     []string // pattern split on "/"
	anchored bool     // pattern contained a "/" other than a trailing one
	dirOnly  bool     // pattern ended in "/"
	negate   bool     // pattern started with "!"
}

func (g *gitignore) enter(root, rel string) error {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), ".gitignore"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for l := range strings.Lines(string(b)) {
		if r, ok := parseRule(rel, strings.TrimRight(l, "\r\n")); ok {
			g.rules = append(g.rules, r)
		}
	}
	return nil
}

func (g *gitignore) decide(rel string, isDir bool) (decision, error) {
	if slices.Contains(g.keep, rel) {
		return include, nil
	}
	ignored := false
	// Rules are appended root first, so later rules come from deeper
	// .gitignore files (or later lines) and win.
	for _, r := range g.rules {
		if r.match(rel, isDir) {
			ignored = !r.negate
		}
	}
	if ignored {
		return skip, nil
	}
	return include, nil
}

// parseRule parses one .gitignore line found in directory base.
func parseRule(base, line string) (rule, bool) {
	line = strings.TrimPrefix(line, "\ufeff")
	// Trailing spaces are ignored unless escaped with a backslash.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
		line = line[:len(line)-1]
	}
	if line == "" || line[0] == '#' {
		return rule{}, false
	}
	r := rule{base: base}
	if line[0] == '!' {
		r.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if strings.Contains(line, "/") {
		r.anchored = true
		line = strings.TrimPrefix(line, "/")
	}
	if line == "" {
		return rule{}, false
	}
	r.segs = strings.Split(line, "/")
	for i, s := range r.segs {
		// git negates a bracket expression with "!", path.Match with "^".
		s = strings.ReplaceAll(s, "[!", "[^")
		if _, err := path.Match(s, ""); err != nil {
			return rule{}, false // malformed pattern; git ignores these too
		}
		r.segs[i] = s
	}
	return r, true
}

func (r rule) match(rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	if r.base != "" {
		var ok bool
		if rel, ok = strings.CutPrefix(rel, r.base+"/"); !ok {
			return false
		}
	}
	if !r.anchored {
		// A pattern without a slash matches the name at any depth.
		return matchSegs(r.segs, []string{path.Base(rel)})
	}
	return matchSegs(r.segs, strings.Split(rel, "/"))
}

// matchSegs matches path segments against pattern segments, where a "**"
// segment matches any number of directories.
func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return len(name) > 0 // "dir/**" matches everything inside dir
			}
			for i := range len(name) + 1 {
				if matchSegs(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
