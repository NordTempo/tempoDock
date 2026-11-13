package registry

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// manifestTypes are the manifest media types the registry is asked for, so
// HEAD and DELETE address what BuildKit pushed, single image or index.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}, ", ")

// Client is the control plane's own connection to the registry API, with
// tokens it mints for each call.
type Client struct {
	// URL is where the control plane reaches the registry, normally
	// http://127.0.0.1:5000.
	URL    string
	Signer *Signer
	// HTTP is the client to use; nil means one with requestTimeout.
	HTTP *http.Client
}

// requestTimeout bounds one registry API call, its body included. The
// calls are small (a HEAD, a DELETE, a page of the catalog), but the
// registry answers only once its storage does: on 2026-11-13 one bucket
// object made every HEAD for it hang. Without a limit, such a call keeps
// a prune waiting for ever while it holds the lock every build and pull
// takes. A minute is far above a healthy answer (milliseconds).
const requestTimeout = time.Minute

// defaultHTTP is the client a Client without HTTP uses.
var defaultHTTP = &http.Client{Timeout: requestTimeout}

// Manifest is one manifest in the registry and the tags that point at it.
type Manifest struct {
	Repo   string
	Digest string
	Tags   []string
}

// Digest returns the digest (sha256:...) of the manifest repo:tag points
// at: what a release records after a build pushed it.
func (c *Client) Digest(ctx context.Context, repo, tag string) (string, error) {
	token := c.Signer.Mint("dock", TokenTTL, Pull(repo))
	return c.digest(ctx, token, repo, tag)
}

func (c *Client) digest(ctx context.Context, token, repo, ref string) (string, error) {
	resp, err := c.do(ctx, http.MethodHead, "/v2/"+repo+"/manifests/"+ref, token)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := check(resp, http.StatusOK); err != nil {
		return "", fmt.Errorf("manifest %s:%s: %w", repo, ref, err)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", fmt.Errorf("manifest %s:%s: the registry sent no digest", repo, ref)
	}
	return d, nil
}

// Prune deletes every tagged manifest whose digest isn't in keep, and the
// tags pointing at it, and returns what it deleted. keep holds digests
// (sha256:...) or digest refs (name@sha256:...): every digest a release
// in a rollback window or an active release uses. Blobs stay on disk
// until GarbageCollect.
//
// An empty keep is refused rather than emptying the registry. Manifests
// pushed without a tag aren't listed by the registry; GarbageCollect's
// --delete-untagged removes them.
func (c *Client) Prune(ctx context.Context, keep []string) ([]Manifest, error) {
	if len(keep) == 0 {
		return nil, errors.New("prune: no digests to keep; refusing to empty the registry")
	}
	manifests, err := c.manifests(ctx)
	if err != nil {
		return nil, err
	}
	var deleted []Manifest
	for _, m := range unreferenced(manifests, keep) {
		token := c.Signer.Mint("dock prune", TokenTTL, deleteAccess(m.Repo))
		resp, err := c.do(ctx, http.MethodDelete, "/v2/"+m.Repo+"/manifests/"+m.Digest, token)
		if err != nil {
			return deleted, err
		}
		err = check(resp, http.StatusAccepted, http.StatusNotFound)
		_ = resp.Body.Close()
		if err != nil {
			return deleted, fmt.Errorf("delete %s@%s: %w", m.Repo, m.Digest, err)
		}
		deleted = append(deleted, m)
	}
	return deleted, nil
}

// unreferenced returns the manifests whose digest keep doesn't mention.
func unreferenced(manifests []Manifest, keep []string) []Manifest {
	kept := make(map[string]bool, len(keep))
	for _, k := range keep {
		if _, d, ok := strings.Cut(k, "@"); ok {
			k = d
		}
		kept[k] = true
	}
	var out []Manifest
	for _, m := range manifests {
		if !kept[m.Digest] {
			out = append(out, m)
		}
	}
	return out
}

// manifests lists every tagged manifest in the registry, sorted by
// repository and digest: one catalog walk, one tag list per repository
// and one HEAD per tag.
func (c *Client) manifests(ctx context.Context) ([]Manifest, error) {
	var repos []string
	err := c.pages(ctx, "/v2/_catalog?n=1000", c.Signer.Mint("dock prune", TokenTTL, catalogAccess), func(body []byte) error {
		var page struct{ Repositories []string }
		err := json.Unmarshal(body, &page)
		repos = append(repos, page.Repositories...)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	var out []Manifest
	for _, repo := range repos {
		token := c.Signer.Mint("dock prune", TokenTTL, deleteAccess(repo))
		var tags []string
		err := c.pages(ctx, "/v2/"+repo+"/tags/list?n=1000", token, func(body []byte) error {
			var page struct{ Tags []string }
			err := json.Unmarshal(body, &page)
			tags = append(tags, page.Tags...)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("list tags of %s: %w", repo, err)
		}
		byDigest := map[string]*Manifest{}
		for _, tag := range tags {
			d, err := c.digest(ctx, token, repo, tag)
			if errors.Is(err, errNotFound) {
				continue // untagged meanwhile
			}
			if err != nil {
				return nil, err
			}
			if m := byDigest[d]; m != nil {
				m.Tags = append(m.Tags, tag)
			} else {
				byDigest[d] = &Manifest{Repo: repo, Digest: d, Tags: []string{tag}}
			}
		}
		for _, m := range byDigest {
			out = append(out, *m)
		}
	}
	slices.SortFunc(out, func(a, b Manifest) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Digest, b.Digest))
	})
	return out, nil
}

// pages GETs path and every next page its Link headers name, passing each
// body to fn.
func (c *Client) pages(ctx context.Context, path, token string, fn func([]byte) error) error {
	for path != "" {
		resp, err := c.do(ctx, http.MethodGet, path, token)
		if err != nil {
			return err
		}
		if err := check(resp, http.StatusOK); err != nil {
			_ = resp.Body.Close()
			return err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if err := fn(body); err != nil {
			return err
		}
		path = nextPage(resp.Header.Get("Link"))
	}
	return nil
}

// nextPage returns the path of a `Link: </v2/...>; rel="next"` header, or "".
func nextPage(link string) string {
	target, params, ok := strings.Cut(link, ";")
	if !ok || !strings.Contains(params, `rel="next"`) {
		return ""
	}
	u, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
	if err != nil {
		return ""
	}
	return u.RequestURI()
}

func (c *Client) do(ctx context.Context, method, path, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", manifestTypes)
	return cmp.Or(c.HTTP, defaultHTTP).Do(req)
}

// errNotFound is a 404 from the registry.
var errNotFound = errors.New("not found in the registry")

// check turns a status outside want into an error with the registry's
// message.
func check(resp *http.Response, want ...int) error {
	if slices.Contains(want, resp.StatusCode) {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	var body struct {
		Errors []struct{ Code, Message string }
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	if len(body.Errors) > 0 {
		return fmt.Errorf("registry: %s: %s", body.Errors[0].Code, body.Errors[0].Message)
	}
	return fmt.Errorf("registry: %s", resp.Status)
}
