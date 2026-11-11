package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// benchInput and benchOutput exercise every part of a request Huma's
// Register handles per call: path and query parameters, a JSON body, an
// output header and body.
type benchInput struct {
	ID    string `path:"id"`
	Limit int    `query:"limit" minimum:"1" maximum:"100" default:"20"`
	Body  struct {
		Name string `json:"name" minLength:"1"`
	}
}

type benchOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
}

// BenchmarkHumaOperation is Huma's per-request cost for one operation:
// parsing and validating the input, calling the handler, writing the
// output. Every API request pays it on top of its handler.
func BenchmarkHumaOperation(b *testing.B) {
	mux := http.NewServeMux()
	a := humago.New(mux, huma.DefaultConfig("bench", "1"))
	huma.Register(a, huma.Operation{OperationID: "bench", Method: http.MethodPut, Path: "/items/{id}"},
		func(_ context.Context, in *benchInput) (*benchOutput, error) {
			out := &benchOutput{ETag: `"1"`}
			out.Body.ID, out.Body.Name, out.Body.Limit = in.ID, in.Body.Name, in.Limit
			return out, nil
		})
	const body = `{"name":"web"}`
	b.ReportAllocs()
	for b.Loop() {
		r := httptest.NewRequest(http.MethodPut, "/items/svc_1?limit=50", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("status %d: %s", w.Code, w.Body)
		}
	}
}
