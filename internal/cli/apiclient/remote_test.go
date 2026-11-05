package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBeforeChangeRunsOnceForChanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.BeforeChange = func() { calls++ }
	ctx := context.Background()

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if err := c.JSON(ctx, method, "/x", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatalf("after GET and HEAD: %d calls, want 0", calls)
	}
	for range 2 {
		if err := c.JSON(ctx, http.MethodPost, "/x", struct{}{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("after two POSTs: %d calls, want 1", calls)
	}
}

func TestRemote(t *testing.T) {
	tests := map[string]bool{
		"http://localhost:7070":         false,
		"http://LOCALHOST":              false,
		"http://127.0.0.1:7070":         false,
		"http://127.1.2.3":              false,
		"http://[::1]:7070":             false,
		"https://dock.example.com":      true,
		"http://192.168.10.50:7070":     true,
		"http://[2001:db8::1]":          true,
		"https://localhost.example.com": true,
	}
	for server, want := range tests {
		c, err := New(server, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Remote(); got != want {
			t.Errorf("Remote(%s) = %v, want %v", server, got, want)
		}
	}
}
