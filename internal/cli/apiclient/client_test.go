package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/cli/output"
)

func TestRequestHeadersAndJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/v1/things" || r.URL.RawQuery != "env=prod" {
			t.Errorf("path = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer tok" || !strings.HasPrefix(r.Header.Get("User-Agent"), "dock-cli/") {
			t.Errorf("headers = %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"web"}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/prefix/", "tok")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Get[struct{ Name string }](context.Background(), c, "/v1/things?env=prod")
	if err != nil || got.Name != "web" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		exit        output.ExitCode
		code        string
		msg         string
	}{
		{"problem detail", 404, "application/problem+json", `{"title":"Not Found","status":404,"detail":"service web not found"}`, output.ExitNotFound, "", "service web not found"},
		{"code overrides status", 422, "application/problem+json", `{"title":"Unprocessable","detail":"health check failed on /healthz after 30s","code":"health_check_failed"}`, output.ExitDeployFailed, "health_check_failed", "health check failed"},
		{"validation errors listed", 422, "application/problem+json", `{"title":"Unprocessable Entity","errors":[{"message":"expected number <= 65535","location":"body.port"}]}`, output.ExitUsage, "", "body.port: expected number <= 65535"},
		{"auth", 401, "application/problem+json", `{"title":"Unauthorized"}`, output.ExitAuth, "", "Unauthorized"},
		{"conflict", 409, "application/problem+json", `{"detail":"a deploy is already running"}`, output.ExitConflict, "", "already running"},
		{"proxy html", 502, "text/html", `<html>bad gateway</html>`, output.ExitUnreachable, "", "Bad Gateway"},
		{"upgrade", 426, "text/plain", `CLI too old`, output.ExitIncompatible, "", "CLI too old"},
		{"server offline", 503, "application/problem+json", `{"detail":"server homelab is offline","code":"server_offline"}`, output.ExitUnreachable, "server_offline", "homelab is offline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "")

			err := c.JSON(context.Background(), http.MethodPost, "/x", map[string]int{"a": 1}, nil)
			e, ok := errors.AsType[*output.Error](err)
			if !ok {
				t.Fatalf("want *output.Error, got %T %v", err, err)
			}
			if e.Exit != tt.exit || e.Code != tt.code || !strings.Contains(e.Error(), tt.msg) {
				t.Errorf("got exit=%d code=%q msg=%q", e.Exit, e.Code, e.Error())
			}
			if tt.code == "server_offline" && !strings.Contains(e.Hint, "dock server list") {
				t.Errorf("hint = %q, want the server_offline one", e.Hint)
			}
			if _, ok := errors.AsType[*Problem](err); !ok {
				t.Error("the Problem should be reachable with errors.As")
			}
		})
	}
}

// Redirects are not followed, so the token never reaches another host.
func TestRedirectsAreNotFollowed(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect was followed (Authorization %q)", r.Header.Get("Authorization"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "secret")
	err := c.JSON(context.Background(), http.MethodGet, "/x", nil, nil)
	if output.ExitCodeOf(err) != output.ExitError || !strings.Contains(err.Error(), other.URL+"/steal") {
		t.Errorf("got %v", err)
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens on this address any more
	c, _ := New(srv.URL, "")
	err := c.JSON(context.Background(), http.MethodGet, "/", nil, nil)
	if output.ExitCodeOf(err) != output.ExitUnreachable {
		t.Errorf("got %v", err)
	}
}

func TestParseServerURL(t *testing.T) {
	for _, s := range []string{"dock.example.com", "ftp://x", "https://", ""} {
		if _, err := ParseServerURL(s); output.ExitCodeOf(err) != output.ExitUsage {
			t.Errorf("%q: want usage error, got %v", s, err)
		}
	}
	u, err := ParseServerURL("https://p.example.com/")
	if err != nil || u.String() != "https://p.example.com" {
		t.Errorf("got %v, %v", u, err)
	}
}
