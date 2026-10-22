package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Hashed assets are gzipped and immutable; index.html (also for client
// routes) is revalidated by ETag; fonts are sent as they are.
func TestUIHandlerCachingAndCompression(t *testing.T) {
	js := strings.Repeat("export const x = 1;\n", 200)
	h := newUIHandler(fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html>" + strings.Repeat("<div></div>", 100))},
		"assets/index-abc.js":   {Data: []byte(js)},
		"assets/font-abc.woff2": {Data: bytes.Repeat([]byte{1, 2, 3}, 400)},
	})
	get := func(path string, hdr ...string) *http.Response {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}

	resp := get("/assets/index-abc.js", "Accept-Encoding", "gzip, br")
	if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("js headers: %v", resp.Header)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != js {
		t.Fatal("gzipped body differs")
	}
	if resp := get("/assets/index-abc.js"); resp.Header.Get("Content-Encoding") != "" || resp.ContentLength != int64(len(js)) {
		t.Fatalf("without Accept-Encoding: %v", resp.Header)
	}
	if resp := get("/assets/index-abc.js", "Accept-Encoding", "gzip;q=0"); resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("gzip;q=0: %v", resp.Header)
	}
	if resp := get("/assets/font-abc.woff2", "Accept-Encoding", "gzip"); resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("font compressed again: %v", resp.Header)
	}

	for _, path := range []string{"/", "/projects/shop"} {
		resp := get(path, "Accept-Encoding", "gzip")
		etag := resp.Header.Get("ETag")
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-cache" || etag == "" {
			t.Fatalf("%s: %d %v", path, resp.StatusCode, resp.Header)
		}
		if resp := get(path, "If-None-Match", etag); resp.StatusCode != http.StatusNotModified {
			t.Fatalf("%s revalidation: %d", path, resp.StatusCode)
		}
	}
	if resp := get("/api/v1/nope"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/api/: %d", resp.StatusCode)
	}
}
