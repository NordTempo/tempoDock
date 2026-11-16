package gateway

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is an in-memory bucket with faults: requests for a key in stall
// hang until the request ends, keys in fail answer 500, and while down
// every request hangs. It ignores signatures.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	stall   map[string]bool // key → hang (HEAD, GET, PUT of it)
	fail    map[string]bool // key → 500
	down    bool
	puts    map[string]int // key → PUTs it took (whole or completed)
	srv     *httptest.Server
	quit    chan struct{} // closed at the end: hanging requests return
}

func newFakeS3(t *testing.T) *fakeS3 {
	f := &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}, stall: map[string]bool{}, fail: map[string]bool{}, puts: map[string]int{}}
	f.quit = make(chan struct{})
	f.srv = httptest.NewServer(f)
	t.Cleanup(func() { close(f.quit); f.srv.Close() })
	return f
}

func (f *fakeS3) set(fn func(f *fakeS3)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakeS3) object(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	return b, ok
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	q := r.URL.Query()
	f.mu.Lock()
	hang := f.down || key != "" && f.stall[key]
	fail := key != "" && f.fail[key]
	f.mu.Unlock()
	if hang {
		select {
		case <-r.Context().Done():
		case <-f.quit:
		}
		return
	}
	if fail {
		http.Error(w, "<Error><Code>InternalError</Code></Error>", http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case key == "" && r.Method == http.MethodGet:
		f.list(w, q)
	case key == "" && r.Method == http.MethodPost && q.Has("delete"):
		var in struct {
			Objects []struct{ Key string } `xml:"Object"`
		}
		_ = xml.NewDecoder(r.Body).Decode(&in)
		for _, o := range in.Objects {
			delete(f.objects, o.Key)
		}
		_, _ = io.WriteString(w, "<DeleteResult></DeleteResult>")
	case r.Method == http.MethodPost && q.Has("uploads"):
		id := strconv.Itoa(len(f.uploads) + 1)
		f.uploads[id] = map[int][]byte{}
		_, _ = fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		n, _ := strconv.Atoi(q.Get("partNumber"))
		b, _ := io.ReadAll(r.Body)
		f.uploads[q.Get("uploadId")][n] = b
		w.Header().Set("ETag", fmt.Sprintf(`"p%d"`, n))
	case r.Method == http.MethodPost && q.Has("uploadId"):
		parts := f.uploads[q.Get("uploadId")]
		var all []byte
		for _, n := range slices.Sorted(func(yield func(int) bool) {
			for n := range parts {
				if !yield(n) {
					return
				}
			}
		}) {
			all = append(all, parts[n]...)
		}
		f.objects[key] = all
		f.puts[key]++
		delete(f.uploads, q.Get("uploadId"))
		_, _ = io.WriteString(w, "<CompleteMultipartUploadResult></CompleteMultipartUploadResult>")
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[key] = b
		f.puts[key]++
	case r.Method == http.MethodHead || r.Method == http.MethodGet:
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			}
			return
		}
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"x"`)
		from := 0
		if rng := r.Header.Get("Range"); rng != "" {
			from, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"))
			if from >= len(b) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				_, _ = io.WriteString(w, "<Error><Code>InvalidRange</Code></Error>")
				return
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)-from))
		if r.Method == http.MethodGet {
			_, _ = w.Write(b[from:])
		}
	default:
		http.Error(w, "unsupported", http.StatusNotImplemented)
	}
}

// list answers ListObjectsV2 (prefix, delimiter, start-after, a token
// that is the last name).
func (f *fakeS3) list(w http.ResponseWriter, q map[string][]string) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	prefix, delim := get("prefix"), get("delimiter")
	after := get("start-after")
	if t := get("continuation-token"); t != "" {
		after = t
	}
	maxKeys, _ := strconv.Atoi(get("max-keys"))
	if maxKeys == 0 {
		maxKeys = 1000
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range f.objects {
			if !yield(k) {
				return
			}
		}
	})
	var names []string
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		name := k
		if delim != "" {
			if i := strings.Index(k[len(prefix):], delim); i >= 0 {
				name = k[:len(prefix)+i+len(delim)]
			}
		}
		if name <= after || len(names) > 0 && names[len(names)-1] == name {
			continue
		}
		names = append(names, name)
	}
	truncated := len(names) > maxKeys
	if truncated {
		names = names[:maxKeys]
	}
	var b strings.Builder
	b.WriteString("<ListBucketResult>")
	for _, n := range names {
		if delim != "" && strings.HasSuffix(n, delim) {
			fmt.Fprintf(&b, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", n)
			continue
		}
		fmt.Fprintf(&b, "<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-11-16T00:00:00Z</LastModified></Contents>", n, len(f.objects[n]))
	}
	if truncated {
		fmt.Fprintf(&b, "<IsTruncated>true</IsTruncated><NextContinuationToken>%s</NextContinuationToken>", names[len(names)-1])
	}
	b.WriteString("</ListBucketResult>")
	_, _ = io.WriteString(w, b.String())
}
