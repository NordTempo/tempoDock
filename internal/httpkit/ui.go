// Package httpkit is HTTP plumbing shared by Tempo Dock's API and
// tempoBase's (ADR 0024): the embedded SPA handler, security headers and
// the CSRF guard, and a token-bucket limiter. It knows nothing about
// either product's domain.
package httpkit

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// UIHandler serves a built SPA from dist: real files when they exist,
// index.html for client-side routes, and placeholder (an HTML page) when
// the UI hasn't been built. Paths under /api/ are 404: the API owns them.
//
// Text assets go out gzip-compressed to clients that accept it. Each file
// is compressed once, on its first request, and kept: dist is embedded and
// finite, so the cache is bounded (about the size of the gzipped UI).
// Vite's hashed files under /assets/ never change and are cached for a
// year; everything else (index.html) is revalidated with an ETag.
func UIHandler(dist fs.FS, placeholder string) http.Handler {
	assets := map[string]*uiAsset{}
	_ = fs.WalkDir(dist, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			assets[p] = &uiAsset{fsys: dist, name: p}
		}
		return nil
	})
	index := assets["index.html"]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if index == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(placeholder))
			return
		}
		a, ok := assets[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			a = index // a client-side route
		}
		a.serve(w, r, ok && strings.HasPrefix(r.URL.Path, "/assets/"))
	})
}

// uiAsset is one file of the UI, prepared on its first request.
type uiAsset struct {
	fsys fs.FS
	name string

	once        sync.Once
	err         error
	size        int
	gz          []byte // nil when compressing doesn't pay
	etag, ctype string
}

// compressible are the file types worth gzipping; fonts and images are
// compressed already.
var compressible = map[string]bool{
	".html": true, ".js": true, ".mjs": true, ".css": true, ".json": true,
	".svg": true, ".txt": true, ".map": true, ".webmanifest": true, ".xml": true,
}

// load computes the asset's ETag and gzipped body. The uncompressed body
// isn't kept: it is read from the embedded files, which are in the binary.
func (a *uiAsset) load() {
	raw, err := fs.ReadFile(a.fsys, a.name)
	if err != nil {
		a.err = err
		return
	}
	a.size = len(raw)
	sum := sha256.Sum256(raw)
	a.etag = `"` + hex.EncodeToString(sum[:12]) + `"`
	ext := path.Ext(a.name)
	if a.ctype = mime.TypeByExtension(ext); a.ctype == "" {
		a.ctype = "application/octet-stream"
	}
	if !compressible[ext] || len(raw) < 512 {
		return
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(raw)
	if zw.Close() == nil && buf.Len() < len(raw)*9/10 {
		a.gz = bytes.Clone(buf.Bytes())
	}
}

// serve writes the asset, gzipped when the client accepts it, or 304 when
// the client's copy is current. immutable marks a content-hashed file.
func (a *uiAsset) serve(w http.ResponseWriter, r *http.Request, immutable bool) {
	a.once.Do(a.load)
	if a.err != nil {
		http.Error(w, "UI file unavailable", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("ETag", a.etag)
	if immutable {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	gzipped := false
	if a.gz != nil {
		h.Set("Vary", "Accept-Encoding")
		if gzipped = acceptsGzip(r.Header.Get("Accept-Encoding")); gzipped {
			h.Set("Content-Encoding", "gzip")
		}
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if gzipped {
		h.Set("Content-Length", strconv.Itoa(len(a.gz)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(a.gz)
		}
		return
	}
	h.Set("Content-Length", strconv.Itoa(a.size))
	if r.Method == http.MethodHead {
		return
	}
	f, err := a.fsys.Open(a.name)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = io.Copy(w, f)
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip.
func acceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if coding != "gzip" && coding != "*" {
			continue
		}
		q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}

// etagMatch reports whether an If-None-Match header lists etag (or *).
// Weak validators match too: the comparison is weak, as for GET.
func etagMatch(header, etag string) bool {
	for part := range strings.SplitSeq(header, ",") {
		t := strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if t == etag || t == "*" {
			return true
		}
	}
	return false
}
