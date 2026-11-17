package caddy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Keyed upstreams (ADR 0091). A route with a Key dials
// {file.<UpstreamsDir>/<key>}: Caddy reads the upstream address from that
// file on every request (and every retry), so switching a service to a new
// release rewrites the file and loads a config identical to the running
// one, which Caddy answers without a reload. A reload closes every idle
// keep-alive connection, and a client sending on one in that moment gets
// EOF; a switch no longer does that. The release in the route's analytics
// tags comes from <key>.release the same way.
//
// A file a config reads exists before that config loads; a file the
// running config already reads changes only after a new config loaded, so
// a rejected config never moves traffic (Proxy.ApplyRoutes).

// keyedRetryFor is how long a keyed route retries a refused connection,
// every holdInterval, before it answers 502: a container that restarts
// after a crash (0.6-2.1 s measured), a stateful service's release that
// starts after its old one stopped (0.7-0.8 s for a small app), or a
// node whose link to the control plane is back after a restart (about
// 1 s). Short enough that a service that stays down fails fast rather
// than reading as a hang. It is the same in every state, so a move's
// hold changes no config: the hold's requests wait in the control plane,
// behind a port that accepts, for up to node.HoldFor (ADR 0091).
const keyedRetryFor = 10 * time.Second

// upstreamFileMode lets Caddy (the directory's group) read the files.
const upstreamFileMode = 0o640

// keyed reports whether r's upstream lives in a file: it has a Key, it
// proxies, and opts keep upstreams in files.
func keyed(opts Options, r node.Route) bool {
	return opts.UpstreamsDir != "" && r.Key != "" && r.RedirectTo == "" && r.Stopped == ""
}

// keyedDial is the dial address of a keyed route. dir is clean
// (validUpstreamsDir), so a plain join is filepath.Join's.
func keyedDial(dir, key string) string { return "{file." + dir + "/" + key + "}" }

// keyedRetry is every keyed route's retry setting, shared: it never
// changes.
var keyedRetry = &loadBalancing{TryDuration: keyedRetryFor.String(), TryInterval: holdInterval.String()}

// keyedTag is an analytics tag ("site.service.release") of a keyed route
// with the release read from <key>.release.
func keyedTag(dir, key, tag string) string {
	return tag[:strings.LastIndexByte(tag, '.')+1] + "{file." + dir + "/" + key + ".release}"
}

// keyedFiles are the files the config of routes reads: path → content.
// Routes with one key must agree on what goes in its files.
func keyedFiles(opts Options, routes []node.Route) (map[string]string, error) {
	files := map[string]string{}
	set := func(path, content string) error {
		if old, ok := files[path]; ok && old != content {
			return fmt.Errorf("routes with key %s disagree: %s or %s", filepath.Base(path), old, content)
		}
		files[path] = content
		return nil
	}
	for _, r := range routes {
		if !keyed(opts, r) {
			continue
		}
		if err := set(filepath.Join(opts.UpstreamsDir, r.Key), r.Upstream); err != nil {
			return nil, err
		}
		for _, a := range r.Analytics {
			if err := set(filepath.Join(opts.UpstreamsDir, r.Key+".release"), a.Tag[strings.LastIndexByte(a.Tag, '.')+1:]); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

// writeUpstreamFile replaces path's content atomically: Caddy reads the
// old file or the new one, never half of one. It is synced before the
// rename, so a crash leaves no empty file for Caddy to dial.
func writeUpstreamFile(path, content string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.WriteString(content)
	if err == nil {
		err = f.Chmod(upstreamFileMode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write upstream %s: %w", path, err)
	}
	return nil
}

// CheckUpstreamsDir reports whether dock can keep upstream files in dir:
// it exists and takes a new file. The daemon checks it once at start and
// keeps upstreams in the config when it fails.
func CheckUpstreamsDir(dir string) error {
	if !validUpstreamsDir(dir) {
		return fmt.Errorf("%q must be an absolute path of letters, digits, '/', '.', '_' and '-'", dir)
	}
	return writeUpstreamFile(filepath.Join(dir, ".check"), "")
}

// validUpstreamsDir keeps the directory a plain absolute path: it goes
// into placeholders, where Caddy would expand "{" or "}".
func validUpstreamsDir(dir string) bool {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return false
	}
	for _, c := range dir {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("/._-", c) {
			return false
		}
	}
	return true
}

// validKey accepts a route key: lower-case letters and digits, as file
// names in the upstreams directory and in placeholders.
func validKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, c := range k {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// upstreamFiles is what the upstreams directory holds, as far as the
// Proxy knows: path → content.
type upstreamFiles map[string]string

// before writes the files want has that don't exist yet, ahead of the
// config that reads them. A file it didn't know is read once, so after a
// restart only a changed one is written, and only after the load.
func (u upstreamFiles) before(want map[string]string) error {
	for path, content := range want {
		if _, ok := u[path]; ok {
			continue
		}
		b, err := os.ReadFile(path)
		if err == nil {
			u[path] = string(b)
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("read upstream %s: %w", path, err)
		}
		if err := writeUpstreamFile(path, content); err != nil {
			return err
		}
		u[path] = content
	}
	return nil
}

// after writes the files want changes, once the config that reads them
// runs, and removes those no route reads any more. sweep also removes
// files in dir the Proxy never knew (a route gone while dock was down).
func (u upstreamFiles) after(dir string, want map[string]string, sweep bool) error {
	var errs []error
	for path, content := range want {
		if u[path] == content {
			continue
		}
		if err := writeUpstreamFile(path, content); err != nil {
			errs = append(errs, err)
			continue
		}
		u[path] = content
	}
	for path := range u {
		if _, ok := want[path]; !ok {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
				continue
			}
			delete(u, path)
		}
	}
	if sweep && dir != "" {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, e := range ents {
			path := filepath.Join(dir, e.Name())
			stale := !strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), ".tmp-")
			if _, ok := want[path]; !ok && stale && !e.IsDir() {
				_ = os.Remove(path)
			}
		}
	}
	return errors.Join(errs...)
}
