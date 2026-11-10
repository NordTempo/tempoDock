package httpkit

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// Hashed assets are immutable and go out as the build gzipped them (or
// decompressed for clients without gzip); index.html (also for client
// routes) is revalidated by ETag; fonts are sent as they are.
func TestUIHandlerCachingAndCompression(t *testing.T) {
	js := strings.Repeat("export const x = 1;\n", 200)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(js))
	_ = zw.Close()
	h := UIHandler(fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html>" + strings.Repeat("<div></div>", 100))},
		"assets/index-abc.js.gz": {Data: gz.Bytes()},
		"assets/font-abc.woff2":  {Data: bytes.Repeat([]byte{1, 2, 3}, 400)},
	}, "not built")
	get := func(path string, hdr ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/assets/index-abc.js", "Accept-Encoding", "gzip, br")
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Header().Get("Content-Length") != strconv.Itoa(gz.Len()) {
		t.Fatalf("js headers: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != js {
		t.Fatal("gzipped body differs")
	}
	if rec := get("/assets/index-abc.js"); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != js || rec.Header().Get("Content-Length") != strconv.Itoa(len(js)) {
		t.Fatalf("without Accept-Encoding: %v", rec.Header())
	}
	if rec := get("/assets/index-abc.js", "Accept-Encoding", "gzip;q=0"); rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("gzip;q=0: %v", rec.Header())
	}
	if rec := get("/assets/font-abc.woff2", "Accept-Encoding", "gzip"); rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("font compressed again: %v", rec.Header())
	}

	for _, path := range []string{"/", "/projects/shop"} {
		rec := get(path, "Accept-Encoding", "gzip")
		etag := rec.Header().Get("ETag")
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" || etag == "" {
			t.Fatalf("%s: %d %v", path, rec.Code, rec.Header())
		}
		if rec := get(path, "If-None-Match", etag); rec.Code != http.StatusNotModified {
			t.Fatalf("%s revalidation: %d", path, rec.Code)
		}
	}
	if rec := get("/assets/index-abc.js.gz"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("the .gz file itself is not a URL: %d %v", rec.Code, rec.Header())
	}
	if rec := get("/api/v1/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("/api/: %d", rec.Code)
	}
}
