// Package httpkit is HTTP plumbing shared by Tempo Dock's API and
// tempoBase's (ADR 0024): the embedded SPA handler, security headers and
// the CSRF guard, and a token-bucket limiter. It knows nothing about
// either product's domain.
package httpkit

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
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
// The build gzips text assets (web/gzipDist.ts): dist holds name.gz in
// place of name, and those bytes go out as they are to clients that
// accept gzip (nearly all), and are decompressed on the fly for the rest.
// Nothing is compressed or kept in memory at run time beyond each file's
// size and ETag. Vite's hashed files under /assets/ never change and are
// cached for a year; everything else (index.html) is revalidated with an
// ETag.
func UIHandler(dist fs.FS, placeholder string) http.Handler {
	assets := map[string]*uiAsset{}
	_ = fs.WalkDir(dist, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if served, ok := strings.CutSuffix(p, ".gz"); ok {
			assets[served] = &uiAsset{fsys: dist, name: p, served: served, gzipped: true}
		} else if assets[p] == nil {
			assets[p] = &uiAsset{fsys: dist, name: p, served: p}
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
	// name is the file in fsys; served the path it answers for (name
	// without .gz when gzipped).
	name, served string
	gzipped      bool

	once sync.Once
	err  error
	// size is the uncompressed size; gzSize the file's when gzipped.
	size, gzSize int
	etag, ctype  string
}

// load computes the asset's sizes, ETag and type. A gzipped file's
// uncompressed size is its gzip trailer's (the UI's files are far below
// 4 GB); its ETag is of the compressed bytes, which the build makes
// reproducibly. Nothing is kept but these.
func (a *uiAsset) load() {
	raw, err := fs.ReadFile(a.fsys, a.name)
	if err != nil {
		a.err = err
		return
	}
	a.size = len(raw)
	if a.gzipped {
		if len(raw) < 18 {
			a.err = errors.New("short gzip file")
			return
		}
		a.gzSize, a.size = len(raw), int(binary.LittleEndian.Uint32(raw[len(raw)-4:]))
	}
	sum := sha256.Sum256(raw)
	a.etag = `"` + hex.EncodeToString(sum[:12]) + `"`
	if a.ctype = mime.TypeByExtension(path.Ext(a.served)); a.ctype == "" {
		a.ctype = "application/octet-stream"
	}
}

// serve writes the asset, gzipped when it is and the client accepts it,
// or 304 when the client's copy is current. immutable marks a
// content-hashed file.
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
	sendGzip := false
	if a.gzipped {
		h.Set("Vary", "Accept-Encoding")
		if sendGzip = acceptsGzip(r.Header.Get("Accept-Encoding")); sendGzip {
			h.Set("Content-Encoding", "gzip")
		}
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	size := a.size
	if sendGzip {
		size = a.gzSize
	}
	h.Set("Content-Length", strconv.Itoa(size))
	if r.Method == http.MethodHead {
		return
	}
	f, err := a.fsys.Open(a.name)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	var body io.Reader = f
	if a.gzipped && !sendGzip {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return
		}
		body = zr
	}
	_, _ = io.Copy(w, body)
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
