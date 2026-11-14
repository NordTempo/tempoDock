// Command templates pins and bumps the images of the app templates
// (internal/apptemplates/catalog, ADR 0081).
//
//	go run ./scripts/templates pin [name...]   pin each image's tag to its current digest
//	go run ./scripts/templates bump [name...]  move each image to the newest tag on its track, then pin
//	go run ./scripts/templates tags IMAGE      list an image's tags, to choose a track
//
// An image is pinned as repo:tag@sha256:<index digest>, and the index must
// have linux/amd64 and linux/arm64. A file whose image changes gets its
// revision raised. It edits the image and revision lines in place, so the
// rest of a file keeps its layout. Run it by hand before a release, then
// e2e/templates.sh for the templates it changed.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const dir = "internal/apptemplates/catalog"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if len(os.Args) == 3 && os.Args[1] == "tags" {
		host, path := registryOf(os.Args[2])
		tags, err := (&registry{host: host, repo: path}).tags(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		slices.SortFunc(tags, compareVersions)
		fmt.Println(strings.Join(tags, "\n"))
		return
	}
	if len(os.Args) < 2 || (os.Args[1] != "pin" && os.Args[1] != "bump") {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/templates pin|bump [name...] | tags IMAGE")
		os.Exit(2)
	}
	names := os.Args[2:]
	if len(names) == 0 {
		files, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
		for _, f := range files {
			names = append(names, strings.TrimSuffix(filepath.Base(f), ".toml"))
		}
	}
	failed := false
	for _, n := range names {
		changed, err := update(ctx, filepath.Join(dir, n+".toml"), os.Args[1] == "bump")
		switch {
		case err != nil:
			failed = true
			fmt.Fprintf(os.Stderr, "%s: %v\n", n, err)
		case changed:
			fmt.Printf("%s: updated\n", n)
		}
	}
	if failed {
		os.Exit(1)
	}
}

var (
	imageLine    = regexp.MustCompile(`(?m)^(\s*image\s*=\s*")([^"]+)(")`)
	trackLine    = regexp.MustCompile(`^\s*track\s*=\s*"([^"]+)"`)
	revisionLine = regexp.MustCompile(`(?m)^revision\s*=\s*(\d+)`)
)

// update pins (and with bump, moves) every image of one template file.
func update(ctx context.Context, path string, bump bool) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(b), "\n")
	changed := false
	for i, line := range lines {
		m := imageLine.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		cur := line[m[4]:m[5]]
		track := ""
		// The track is the next track line of the same service.
		for _, next := range lines[i+1:] {
			if strings.Contains(next, "[[services]]") {
				break
			}
			if t := trackLine.FindStringSubmatch(next); t != nil {
				track = t[1]
				break
			}
		}
		next, err := resolve(ctx, cur, track, bump)
		if err != nil {
			return false, fmt.Errorf("%s: %w", cur, err)
		}
		if next != cur {
			lines[i] = line[:m[4]] + next + line[m[5]:]
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	out := strings.Join(lines, "\n")
	out = revisionLine.ReplaceAllStringFunc(out, func(s string) string {
		n, _ := strconv.Atoi(revisionLine.FindStringSubmatch(s)[1])
		return "revision = " + strconv.Itoa(n+1)
	})
	return true, os.WriteFile(path, []byte(out), 0o644)
}

// resolve returns ref pinned: with bump, at the newest tag on track.
func resolve(ctx context.Context, ref, track string, bump bool) (string, error) {
	name, _, _ := strings.Cut(ref, "@")
	repo, tag := name, "latest"
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		repo, tag = name[:i], name[i+1:]
	}
	host, path := registryOf(repo)
	r := &registry{host: host, repo: path}
	if bump {
		if track == "" {
			return "", errors.New("no track to bump along")
		}
		re, err := regexp.Compile(track)
		if err != nil {
			return "", fmt.Errorf("track: %w", err)
		}
		tags, err := r.tags(ctx)
		if err != nil {
			return "", err
		}
		var on []string
		for _, t := range tags {
			if re.MatchString(t) {
				on = append(on, t)
			}
		}
		if len(on) == 0 {
			return "", fmt.Errorf("no tag matches %s", track)
		}
		slices.SortFunc(on, compareVersions)
		tag = on[len(on)-1]
	}
	digest, err := r.index(ctx, tag)
	if err != nil {
		return "", err
	}
	return repo + ":" + tag + "@" + digest, nil
}

// registryOf splits an image repository into its registry host and path.
func registryOf(repo string) (string, string) {
	first, rest, ok := strings.Cut(repo, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first, rest
	}
	if !ok {
		return "registry-1.docker.io", "library/" + repo
	}
	return "registry-1.docker.io", repo
}

var digits = regexp.MustCompile(`\d+`)

// compareVersions orders tags by their numbers (1.10 after 1.9).
func compareVersions(a, b string) int {
	x, y := digits.FindAllString(a, -1), digits.FindAllString(b, -1)
	for i := range min(len(x), len(y)) {
		p, _ := strconv.Atoi(x[i])
		q, _ := strconv.Atoi(y[i])
		if p != q {
			return cmp.Compare(p, q)
		}
	}
	return cmp.Or(cmp.Compare(len(x), len(y)), cmp.Compare(a, b))
}

// registry is an anonymous client for one repository.
type registry struct {
	host, repo, token string
}

const indexTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json"

// index returns the digest of tag's image index, checking it has both
// platforms Dock servers run.
func (r *registry) index(ctx context.Context, tag string) (string, error) {
	resp, err := r.get(ctx, "/manifests/"+tag, indexTypes)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var idx struct {
		Manifests []struct {
			Platform struct{ OS, Architecture string }
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return "", err
	}
	have := map[string]bool{}
	for _, m := range idx.Manifests {
		have[m.Platform.OS+"/"+m.Platform.Architecture] = true
	}
	for _, p := range []string{"linux/amd64", "linux/arm64"} {
		if !have[p] {
			return "", fmt.Errorf("tag %s has no %s image", tag, p)
		}
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", errors.New("the registry sent no digest")
	}
	return d, nil
}

// tags lists the repository's tags, following pages.
func (r *registry) tags(ctx context.Context) ([]string, error) {
	var out []string
	next := "/tags/list?n=1000"
	for next != "" {
		resp, err := r.get(ctx, next, "application/json")
		if err != nil {
			return nil, err
		}
		var page struct{ Tags []string }
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, page.Tags...)
		next = ""
		if link := resp.Header.Get("Link"); link != "" {
			if u, _, ok := strings.Cut(strings.TrimPrefix(link, "<"), ">"); ok {
				next = strings.TrimPrefix(u, "/v2/"+r.repo)
			}
		}
	}
	return out, nil
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// get requests a path under the repository, fetching an anonymous token
// when the registry asks for one.
func (r *registry) get(ctx context.Context, path, accept string) (*http.Response, error) {
	for range 2 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+r.host+"/v2/"+r.repo+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && r.token == "" {
			challenge := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			if err := r.login(ctx, challenge); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("%s: %s %s", path, resp.Status, strings.TrimSpace(string(body)))
		}
		return resp, nil
	}
	return nil, errors.New("the registry refused the anonymous token")
}

// login gets an anonymous pull token from the challenge's realm.
func (r *registry) login(ctx context.Context, challenge string) error {
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if params["realm"] == "" {
		return fmt.Errorf("unexpected challenge %q", challenge)
	}
	q := url.Values{"scope": {"repository:" + r.repo + ":pull"}}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var tok struct{ Token, AccessToken string }
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return err
	}
	r.token = cmp.Or(tok.Token, tok.AccessToken)
	if r.token == "" {
		return errors.New("no token in the registry's answer")
	}
	return nil
}
