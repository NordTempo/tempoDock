package api

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// envOp is a mutating API operation inside an environment, with a request
// that passes Huma's validation and reaches core.
type envOp struct {
	id, method, path string
	body             any
}

// envOps lists every operation of the API that changes something under
// /projects/{project}/environments/{environment}: the paths are filled in
// for shop/production/web, and request bodies made from their schemas.
// New operations are picked up without listing them.
func envOps(t *testing.T) []envOp {
	t.Helper()
	a := newAPI(http.NewServeMux(), &Server{})
	reg := a.OpenAPI().Components.Schemas
	params := regexp.MustCompile(`\{([a-z_]+)\}`)
	var ops []envOp
	for path, item := range a.OpenAPI().Paths {
		if !strings.Contains(path, "/environments/{environment}") {
			continue
		}
		for method, op := range map[string]*huma.Operation{"POST": item.Post, "PUT": item.Put, "PATCH": item.Patch, "DELETE": item.Delete} {
			if op == nil {
				continue
			}
			p := params.ReplaceAllStringFunc(path, func(m string) string {
				switch m {
				case "{project}":
					return "shop"
				case "{environment}":
					return "production"
				case "{service}":
					return "web"
				case "{number}", "{id}", "{snapshot}":
					return "1"
				}
				return "x"
			})
			var body any
			if rb := op.RequestBody; rb != nil {
				if mt := rb.Content["application/json"]; mt != nil && mt.Schema != nil {
					body = sample(reg, mt.Schema)
				}
			}
			ops = append(ops, envOp{op.OperationID, method, p, body})
		}
	}
	slices.SortFunc(ops, func(a, b envOp) int { return strings.Compare(a.id, b.id) })
	return ops
}

// sample is the smallest value that satisfies s: required properties only,
// enums' first value, minimums.
func sample(reg huma.Registry, s *huma.Schema) any {
	if s.Ref != "" {
		s = reg.SchemaFromRef(s.Ref)
	}
	if len(s.Enum) > 0 {
		return s.Enum[0]
	}
	switch s.Type {
	case huma.TypeObject:
		m := map[string]any{}
		for _, k := range s.Required {
			m[k] = sample(reg, s.Properties[k])
		}
		return m
	case huma.TypeArray:
		n := 0
		if s.MinItems != nil {
			n = *s.MinItems
		}
		out := make([]any, n)
		for i := range out {
			out[i] = sample(reg, s.Items)
		}
		return out
	case huma.TypeInteger, huma.TypeNumber:
		if s.Minimum != nil {
			return max(*s.Minimum, 1)
		}
		return 1
	case huma.TypeBoolean:
		return false
	case huma.TypeString:
		switch s.Format {
		case "date-time":
			return "2026-01-01T00:00:00Z"
		case "uri", "url":
			return "https://x.example.com"
		}
		n := 1
		if s.MinLength != nil {
			n = max(n, *s.MinLength)
		}
		v := strings.Repeat("x", n)
		if s.Pattern != "" {
			re := regexp.MustCompile(s.Pattern)
			for _, c := range []string{v, "/" + v, "x.example.com", "X"} {
				if re.MatchString(c) {
					return c
				}
			}
		}
		return v
	}
	return nil
}

// TestLockedEnvironmentRefusesTokens is the lock's permission matrix
// (ADR 0044): every mutating operation in a locked environment answers
// 423 environment_locked to tokens of every scope that could otherwise
// try it, while the same environment unlocked, and a person in a UI
// session, never meet the lock.
func TestLockedEnvironmentRefusesTokens(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.core.LockEnvironment(ctx, sys, core.EnvRef{Project: "shop", Environment: "production"}, apitypes.LockEnvironmentRequest{Reason: "live"}); err != nil {
		t.Fatal(err)
	}
	ops := envOps(t)
	if len(ops) < 30 {
		t.Fatalf("found %d environment operations; the spec walk is broken", len(ops))
	}
	// Locking is the one change a lock allows: it only takes power away.
	// The Data panel's writes are capped at proposing instead (core's
	// TestLockedDataProposes); they need a database to reach that far.
	// Cloning reads it and creates another environment.
	skip := func(op envOp) bool {
		return op.id == "lock-environment" || op.id == "clone-environment" || strings.Contains(op.path, "/data/")
	}
	for _, op := range ops {
		if skip(op) {
			continue
		}
		// Tokens of every scope that may try it meet the lock; scopes
		// that may not (deploy on an admin action, admins on an
		// operator's) are refused first.
		locked := 0
		for _, tok := range []string{"operator", "admin", "deploy", "shop-deploy"} {
			resp, body := f.do(tok, op.method, op.path, op.body)
			if op.id == "deploy-source" { // a build context, which is kept for approval
				resp, body = f.upload(tok, op.path)
			}
			var p apitypes.Error
			_ = json.Unmarshal(body, &p)
			switch {
			case resp.StatusCode == http.StatusLocked && p.Code == apitypes.CodeEnvironmentLocked:
				locked++
			case resp.StatusCode != http.StatusForbidden:
				t.Errorf("%s %s (%s) as %s: %d %s, want 423 environment_locked", op.method, op.path, op.id, tok, resp.StatusCode, body)
			}
		}
		if locked == 0 {
			t.Errorf("%s %s (%s): no token met the lock", op.method, op.path, op.id)
		}
	}
	// Reads stay allowed.
	if resp, body := f.do("deploy", "GET", shopProd+"/services/web/releases", nil); resp.StatusCode != 200 {
		t.Fatalf("read in a locked environment: %d %s", resp.StatusCode, body)
	}

	// A person in the UI isn't stopped by the lock; neither is a token in
	// an unlocked environment (blog/production). Whatever else they meet,
	// it's never the lock.
	admin := f.login()
	for _, op := range ops {
		if skip(op) {
			continue
		}
		if resp, body := f.as(admin, op.method, op.path, op.body); resp.StatusCode == http.StatusLocked {
			t.Errorf("UI session: %s %s: %s", op.method, op.path, body)
		}
		blog := strings.Replace(strings.Replace(op.path, "/shop/", "/blog/", 1), "/services/web", "/services/api", 1)
		if resp, body := f.do("admin", op.method, blog, op.body); resp.StatusCode == http.StatusLocked {
			t.Errorf("unlocked: %s %s: %s", op.method, blog, body)
		}
	}
}

// TestLockOpensForAWhile: an opened environment lets tokens in until its
// time is up, then the lock holds again without anything running.
func TestLockOpensForAWhile(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do("admin", "POST", shopProd+"/lock", apitypes.LockEnvironmentRequest{Reason: "live"})
	var e apitypes.Environment
	_ = json.Unmarshal(body, &e)
	if resp.StatusCode != 200 || e.Lock == nil || e.Lock.Reason != "live" || e.Lock.LockedBy != "admin" {
		t.Fatalf("lock: %d %s", resp.StatusCode, body)
	}
	// A token can't unlock: that is a change like any other.
	if resp, body := f.do("admin", "POST", shopProd+"/unlock", apitypes.UnlockEnvironmentRequest{}); resp.StatusCode != http.StatusLocked {
		t.Fatalf("token unlock: %d %s", resp.StatusCode, body)
	}
	admin := f.login()
	resp, body = f.as(admin, "POST", shopProd+"/unlock", apitypes.UnlockEnvironmentRequest{Minutes: 30})
	_ = json.Unmarshal(body, &e)
	if resp.StatusCode != 200 || e.Lock == nil || e.Lock.OpenUntil == nil {
		t.Fatalf("open: %d %s", resp.StatusCode, body)
	}
	set := apitypes.SetVariablesRequest{Set: map[string]string{"A": "1"}}
	if resp, body := f.do("deploy", "PATCH", shopProd+"/variables", set); resp.StatusCode != 200 {
		t.Fatalf("while open: %d %s", resp.StatusCode, body)
	}
	resp, body = f.as(admin, "POST", shopProd+"/unlock", apitypes.UnlockEnvironmentRequest{})
	var unlocked apitypes.Environment
	_ = json.Unmarshal(body, &unlocked)
	if resp.StatusCode != 200 || unlocked.Lock != nil {
		t.Fatalf("unlock: %d %s", resp.StatusCode, body)
	}
	_, body = f.do("admin", "GET", "/api/v1/audit?action=environment.unlock", nil)
	if !strings.Contains(string(body), "opened for 30m0s") {
		t.Fatalf("audit: %s", body)
	}
}

// TestAccessRequestFlow: a token's refused change answers 423 naming the
// request it filed, with the reason it sent; an admin in the UI sees it
// with its arguments, approves it, and it has run as the token.
func TestAccessRequestFlow(t *testing.T) {
	f := newFixture(t)
	if _, err := f.core.LockEnvironment(context.Background(), sys, core.EnvRef{Project: "shop", Environment: "production"}, apitypes.LockEnvironmentRequest{}); err != nil {
		t.Fatal(err)
	}
	set := apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"PRICE": "42"}}
	req, _ := http.NewRequest("PATCH", f.srv.URL+shopProd+"/variables", strings.NewReader(`{"service":"web","set":{"PRICE":"42"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.tokens["deploy"])
	req.Header.Set("Dock-Reason", "launch prices")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var p apitypes.Error
	_ = json.NewDecoder(resp.Body).Decode(&p)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusLocked || p.Request == 0 || !strings.Contains(p.Detail, "dock request wait") {
		t.Fatalf("refused: %d %+v", resp.StatusCode, p)
	}
	// The same call again joins it.
	if _, body := f.do("deploy", "PATCH", shopProd+"/variables", set); !strings.Contains(string(body), fmt.Sprintf(`"request":%d`, p.Request)) {
		t.Fatalf("retry: %s", body)
	}

	admin := f.login()
	_, body := f.as(admin, "GET", "/api/v1/requests?status=pending", nil)
	var list []apitypes.AccessRequest
	_ = json.Unmarshal(body, &list)
	if len(list) != 1 || list[0].ID != p.Request || list[0].Reason != "launch prices" || list[0].Operation != "set-variables" {
		t.Fatalf("inbox: %s", body)
	}
	_, body = f.as(admin, "GET", fmt.Sprintf("/api/v1/requests/%d", p.Request), nil)
	if !strings.Contains(string(body), `"args":`) || !strings.Contains(string(body), `"PRICE":"42"`) {
		t.Fatalf("detail: %s", body)
	}
	// A token never approves.
	if resp, _ := f.do("admin", "POST", fmt.Sprintf("/api/v1/requests/%d/approve", p.Request), apitypes.DecideAccessRequest{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("token approves: %d", resp.StatusCode)
	}
	resp2, body := f.as(admin, "POST", fmt.Sprintf("/api/v1/requests/%d/approve", p.Request), apitypes.DecideAccessRequest{Comment: "ok"})
	var done apitypes.AccessRequest
	_ = json.Unmarshal(body, &done)
	if resp2.StatusCode != 200 || done.Status != apitypes.RequestDone {
		t.Fatalf("approve: %d %s", resp2.StatusCode, body)
	}
	_, body = f.do("deploy", "GET", fmt.Sprintf("/api/v1/requests/%d?wait=1", p.Request), nil)
	if !strings.Contains(string(body), `"status":"done"`) || strings.Contains(string(body), `"args"`) {
		t.Fatalf("token's view: %s", body)
	}
	_, body = f.do("admin", "GET", shopProd+"/changes", nil)
	if !strings.Contains(string(body), "PRICE") {
		t.Fatalf("not staged: %s", body)
	}
}

// upload sends a one-file build context to a deploy-source path.
func (f *fixture) upload(token, path string) (result, []byte) {
	f.t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "server.js", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	req, _ := http.NewRequest("POST", f.srv.URL+path, &buf)
	req.Header.Set("Content-Type", "application/x-tar")
	req.Header.Set("Authorization", "Bearer "+f.tokens[token])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{StatusCode: resp.StatusCode, Header: resp.Header}, b
}
