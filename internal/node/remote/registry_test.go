package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	reg := newFakeRegistry(t)
	h := newHarness(t, func(o *HubOptions) { o.Registry = reg.addr })
	base, client := h.serveRegistry()
	ctx := context.Background()

	// get makes a request like Docker's, with a registry token.
	get := func(t *testing.T, method, path string, body io.Reader) (int, http.Header, []byte) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, base+path, body)
		req.Header.Set("Authorization", "Bearer pull-token")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, b
	}

	t.Run("manifest", func(t *testing.T) {
		status, hdr, body := get(t, http.MethodGet, "/v2/shop/web/manifests/v1", nil)
		if status != http.StatusOK || string(body) != reg.manifest || hdr.Get("Docker-Content-Digest") == "" {
			t.Fatalf("GET manifest = %d %q %v", status, body, hdr)
		}
		status, hdr, body = get(t, http.MethodHead, "/v2/shop/web/manifests/v1", nil)
		if status != http.StatusOK || hdr.Get("Content-Length") != fmt.Sprint(len(reg.manifest)) || len(body) != 0 {
			t.Fatalf("HEAD manifest = %d %v", status, hdr)
		}
		// The proxy adds nothing: without the token, the registry says no.
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/shop/web/manifests/v1", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("no token = %d", resp.StatusCode)
		}
	})

	t.Run("large blob", func(t *testing.T) {
		status, _, blob := get(t, http.MethodGet, "/v2/shop/web/blobs/big", nil)
		sum := sha256.Sum256(blob)
		if status != http.StatusOK || hex.EncodeToString(sum[:]) != reg.blobSum {
			t.Fatalf("blob: %d, %d of %d bytes", status, len(blob), len(reg.blob))
		}
	})

	t.Run("chunked upload", func(t *testing.T) {
		payload := make([]byte, 6<<20)
		_, _ = rand.Read(payload)
		status, hdr, _ := get(t, http.MethodPost, "/v2/shop/web/blobs/uploads/", nil)
		loc := hdr.Get("Location")
		if status != http.StatusAccepted || loc == "" {
			t.Fatalf("start upload = %d", status)
		}
		for chunk := range slicesChunk(payload, 2<<20) {
			if status, _, _ := get(t, http.MethodPatch, loc, bytes.NewReader(chunk)); status != http.StatusAccepted {
				t.Fatalf("PATCH = %d", status)
			}
		}
		sum := sha256.Sum256(payload)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if status, _, _ := get(t, http.MethodPut, loc+"?digest="+digest, nil); status != http.StatusCreated {
			t.Fatalf("PUT = %d", status)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, base+"/v2/shop/web/blobs/endless", nil)
		req.Header.Set("Authorization", "Bearer pull-token")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(io.Discard, resp.Body, 4<<20); err != nil {
			t.Fatal(err)
		}
		cancel()
		_ = resp.Body.Close()
		// Docker going away reaches the registry: its handler is canceled
		// and its connection closed, and the node's tunnel is free again.
		select {
		case <-reg.endlessCanceled:
		case <-time.After(5 * time.Second):
			t.Fatal("the registry kept sending after the client canceled")
		}
		client.CloseIdleConnections()
		waitFor(t, "registry connections to close", func() bool { return reg.open.Load() == 0 })
		if status, _, _ := get(t, http.MethodHead, "/v2/shop/web/manifests/v1", nil); status != http.StatusOK {
			t.Fatalf("after cancel: %d", status)
		}
	})

	t.Run("control plane offline", func(t *testing.T) {
		client.CloseIdleConnections()
		h.stopNode()
		waitFor(t, "disconnect", func() bool { return !h.client.Online() })
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", nil)
		c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		// Not served: nothing answers (the node holds it up to
		// registryWait, then closes; closing with the request unread
		// resets the connection).
		_ = req.Write(c)
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) &&
			!errors.Is(err, syscall.ECONNRESET) && !isTimeout(err) {
			t.Fatalf("read = %v", err)
		}
	})
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func slicesChunk(b []byte, n int) func(func([]byte) bool) {
	return func(yield func([]byte) bool) {
		for len(b) > 0 {
			c := b[:min(n, len(b))]
			b = b[len(c):]
			if !yield(c) {
				return
			}
		}
	}
}

// serveRegistry runs the node's registry listener and returns its base URL
// and a client like Docker's.
func (h *harness) serveRegistry() (string, *http.Client) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	n := h.node
	go func() { _ = n.ServeRegistry(ctx, ln); close(done) }()
	tr := &http.Transport{}
	h.t.Cleanup(func() { tr.CloseIdleConnections(); cancel(); <-done })
	return "http://" + ln.Addr().String(), &http.Client{Transport: tr}
}

// fakeRegistry is enough of the registry API for the tunnel: manifests,
// blobs and chunked uploads, all requiring a bearer token.
type fakeRegistry struct {
	addr            string
	manifest        string
	blob            []byte
	blobSum         string
	open            atomic.Int32
	endlessCanceled chan struct{}

	mu      sync.Mutex
	uploads map[string]*bytes.Buffer
}

func newFakeRegistry(t testing.TB) *fakeRegistry {
	r := &fakeRegistry{
		manifest: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`,
		blob:     make([]byte, 64<<20), uploads: map[string]*bytes.Buffer{},
		endlessCanceled: make(chan struct{}, 1),
	}
	_, _ = rand.Read(r.blob)
	sum := sha256.Sum256(r.blob)
	r.blobSum = hex.EncodeToString(sum[:])
	srv := httptest.NewUnstartedServer(http.HandlerFunc(r.serve))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			r.open.Add(1)
		case http.StateClosed, http.StateHijacked:
			r.open.Add(-1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	r.addr = srv.Listener.Addr().String()
	return r
}

func (r *fakeRegistry) serve(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Authorization") != "Bearer pull-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := req.URL.Path
	switch {
	case p == "/v2/shop/web/manifests/v1":
		w.Header().Set("Docker-Content-Digest", "sha256:abc")
		w.Header().Set("Content-Length", fmt.Sprint(len(r.manifest)))
		if req.Method == http.MethodGet {
			_, _ = io.WriteString(w, r.manifest)
		}
	case p == "/v2/shop/web/blobs/big":
		_, _ = w.Write(r.blob)
	case p == "/v2/shop/web/blobs/endless":
		chunk := make([]byte, 64<<10)
		for {
			if _, err := w.Write(chunk); err != nil {
				r.endlessCanceled <- struct{}{}
				return
			}
		}
	case p == "/v2/shop/web/blobs/uploads/" && req.Method == http.MethodPost:
		r.mu.Lock()
		id := fmt.Sprint(len(r.uploads) + 1)
		r.uploads[id] = &bytes.Buffer{}
		r.mu.Unlock()
		w.Header().Set("Location", "/v2/shop/web/blobs/uploads/"+id)
		w.WriteHeader(http.StatusAccepted)
	case strings.HasPrefix(p, "/v2/shop/web/blobs/uploads/"):
		r.mu.Lock()
		buf := r.uploads[strings.TrimPrefix(p, "/v2/shop/web/blobs/uploads/")]
		r.mu.Unlock()
		if buf == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch req.Method {
		case http.MethodPatch:
			_, _ = io.Copy(buf, req.Body)
			w.Header().Set("Range", fmt.Sprintf("0-%d", buf.Len()-1))
			w.WriteHeader(http.StatusAccepted)
		case http.MethodPut:
			sum := sha256.Sum256(buf.Bytes())
			if req.URL.Query().Get("digest") != "sha256:"+hex.EncodeToString(sum[:]) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
