package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

const dbPath = "/api/v1/projects/shop/environments/production/services/db/data"

var dbTarget = []string{"-p", "shop", "-e", "production", "-s", "db"}

// fakeAPI serves canned responses by "METHOD /path" and records request
// bodies, so CLI output and exit codes can be checked without a daemon.
type fakeAPI struct {
	bodies map[string]string // "METHOD /path" → last request body
}

func newFakeAPI(t *testing.T, routes map[string]http.HandlerFunc) *fakeAPI {
	t.Helper()
	f := &fakeAPI{bodies: map[string]string{}}
	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			f.bodies[pattern] = string(b)
			r.Body = io.NopCloser(bytes.NewReader(b))
			h(w, r)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(clientconfig.EnvURL, srv.URL)
	t.Setenv(clientconfig.EnvToken, "dock_test")
	return f
}

func jsonReply(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func problemReply(status int, code apitypes.ErrorCode, detail string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(apitypes.Error{Status: status, Code: code, Detail: detail})
	}
}

func ndjsonReply(frames ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, f := range frames {
			_, _ = io.WriteString(w, f+"\n")
		}
	}
}

func dbArgs(args ...string) []string { return append(args, dbTarget...) }

func TestDBQueryOutput(t *testing.T) {
	setup(t)
	frames := []string{
		`{"type":"header","columns":[{"name":"id","type_name":"int4"},{"name":"note","type_name":"text"}]}`,
		`{"type":"rows","rows":[[1,"short"],[2,null]]}`,
		`{"type":"rows","rows":[[3,"a much longer note than any before it, cut to fit the column"],[4,{"masked":true}]]}`,
		`{"type":"end","row_count":4,"truncated":true,"command":"SELECT 4","duration_ms":3}`,
	}
	api := newFakeAPI(t, map[string]http.HandlerFunc{"POST " + dbPath + "/query": ndjsonReply(frames...)})

	code, stdout, stderr := exec(t, dbArgs("db", "query", "select id, note from notes", "--timeout", "1500ms")...)
	if code != 0 {
		t.Fatalf("query = %d\n%s", code, stderr)
	}
	if want := "ID  NOTE\n1   short\n2   NULL\n3   a mu…\n4   (mas…\n"; stdout != want {
		t.Errorf("table =\n%s\nwant\n%s", stdout, want)
	}
	if !strings.Contains(stderr, "4 rows in 3 ms") || !strings.Contains(stderr, "cut at 4 rows") {
		t.Errorf("summary = %q", stderr)
	}
	var req apitypes.DataQueryRequest
	_ = json.Unmarshal([]byte(api.bodies["POST "+dbPath+"/query"]), &req)
	if req.SQL != "select id, note from notes" || req.Limit != 1000 || req.TimeoutSeconds != 2 {
		t.Errorf("request = %+v", req)
	}

	_, stdout, _ = exec(t, dbArgs("db", "query", "select 1", "--csv")...)
	if !strings.HasPrefix(stdout, "id,note\n1,short\n2,\n3,\"a much longer note") {
		t.Errorf("csv =\n%s", stdout)
	}

	_, stdout, _ = exec(t, dbArgs("db", "query", "select 1", "--json")...)
	if stdout != strings.Join(frames, "\n")+"\n" {
		t.Errorf("--json should pass the frames through, got\n%s", stdout)
	}
}

func TestDBQueryErrors(t *testing.T) {
	setup(t)
	newFakeAPI(t, map[string]http.HandlerFunc{
		"POST " + dbPath + "/query": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			switch {
			case strings.Contains(string(b), "nope"):
				ndjsonReply(`{"type":"header","columns":[]}`,
					`{"type":"error","error":{"message":"relation \"nope\" does not exist","sqlstate":"42P01","position":15}}`)(w, r)
			case strings.Contains(string(b), "delete"):
				ndjsonReply(`{"type":"error","error":{"message":"cannot execute DELETE in a read-only transaction","sqlstate":"25006"}}`)(w, r)
			case strings.Contains(string(b), "forbidden"):
				problemReply(403, apitypes.CodeForbidden, "reading data needs level read; built-in default: role:deploy has none on production")(w, r)
			default:
				problemReply(503, apitypes.CodeServerOffline, "the database is not running")(w, r)
			}
		},
	})
	cases := []struct {
		sql    string
		exit   output.ExitCode
		stderr string
	}{
		{"select * from nope", output.ExitUsage, `relation "nope" does not exist (SQLSTATE 42P01) at character 15`},
		{"delete from t", output.ExitAuth, "dock db apply"},
		{"forbidden", output.ExitAuth, "role:deploy has none on production"},
		{"down", output.ExitUnreachable, "dock db status"},
	}
	for _, c := range cases {
		code, _, stderr := exec(t, dbArgs("db", "query", c.sql)...)
		if code != int(c.exit) || !strings.Contains(stderr, c.stderr) {
			t.Errorf("%q: exit %d, stderr %q; want %d mentioning %q", c.sql, code, stderr, c.exit, c.stderr)
		}
	}
	if code, _, _ := exec(t, dbArgs("db", "query", "select 1", "--timeout", "2m")...); code != int(output.ExitUsage) {
		t.Errorf("--timeout over the max = %d, want usage", code)
	}
}

func TestDBRows(t *testing.T) {
	setup(t)
	var query string
	newFakeAPI(t, map[string]http.HandlerFunc{
		"GET " + dbPath + "/tables/public.orders/rows": func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			jsonReply(apitypes.DataPage{
				Columns:       []apitypes.DataResultColumn{{Name: "id"}, {Name: "status"}},
				Rows:          []apitypes.DataRow{json.RawMessage(`[1041,"paid"]`)},
				Next:          "CURSOR2",
				EstimatedRows: 12431,
			})(w, r)
		},
	})
	code, stdout, stderr := exec(t, dbArgs("db", "rows", "orders", "--filter", "status=paid", "-w", "total=gt:100", "-w", "id=in:1,2", "-w", "deleted_at=is_null", "--order", "-id")...)
	if code != 0 || !strings.Contains(stdout, "1041  paid") || !strings.Contains(stderr, "--after CURSOR2") || !strings.Contains(stderr, "1 row of ~12431") {
		t.Fatalf("rows = %d\n%s\n%s", code, stdout, stderr)
	}
	q, _ := url.ParseQuery(query)
	var filters []apitypes.DataFilter
	_ = json.Unmarshal([]byte(q.Get("filter")), &filters)
	want := []apitypes.DataFilter{
		{Column: "status", Op: apitypes.FilterEq, Value: "paid"},
		{Column: "total", Op: apitypes.FilterGt, Value: "100"},
		{Column: "id", Op: apitypes.FilterIn, Values: []string{"1", "2"}},
		{Column: "deleted_at", Op: apitypes.FilterIsNull},
	}
	if len(filters) != len(want) || q.Get("order") != "-id" {
		t.Fatalf("query = %s", query)
	}
	for i := range want {
		if filters[i].Column != want[i].Column || filters[i].Op != want[i].Op || filters[i].Value != want[i].Value || strings.Join(filters[i].Values, ",") != strings.Join(want[i].Values, ",") {
			t.Errorf("filter %d = %+v, want %+v", i, filters[i], want[i])
		}
	}
}

func TestDBApply(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	file := dir + "/changes.json"
	if err := os.WriteFile(file, []byte(`[{"op":"update","table":"public.orders","key":{"id":"1"},"old":{"total":"10"},"new":{"total":"12"}}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	level := apitypes.LevelPropose
	committed := false
	newFakeAPI(t, map[string]http.HandlerFunc{
		"GET " + dbPath: func(w http.ResponseWriter, r *http.Request) {
			jsonReply(apitypes.DataInfo{Access: apitypes.DataPermission{Level: level, ConfirmCommit: true}})(w, r)
		},
		"POST " + dbPath + "/changesets": jsonReply(apitypes.DataChangeset{ID: 9, Status: apitypes.ChangesetPending, ConfirmCommit: true,
			Changes: []apitypes.DataChange{{Op: apitypes.DataChangeUpdate}}}),
		"POST " + dbPath + "/changesets/9/commit": func(w http.ResponseWriter, r *http.Request) {
			committed = true
			jsonReply(apitypes.DataChangeset{ID: 9, Status: apitypes.ChangesetConflict,
				Conflicts: []apitypes.DataConflict{{Change: 0, Reason: apitypes.ConflictChanged, Message: "total is 11 now"}}})(w, r)
		},
	})

	// Level propose: the changeset waits for an admin.
	code, _, stderr := exec(t, dbArgs("db", "apply", "-f", file)...)
	if code != 0 || committed || !strings.Contains(stderr, "dock db changes commit 9") {
		t.Fatalf("propose = %d committed=%v\n%s", code, committed, stderr)
	}

	// Level write on production: no --confirm and no terminal is a usage
	// error; with it, a conflict exits 5.
	level = apitypes.LevelWrite
	if code, _, stderr := exec(t, dbArgs("db", "apply", "-f", file)...); code != int(output.ExitUsage) || !strings.Contains(stderr, "--confirm") {
		t.Errorf("commit without --confirm = %d %s", code, stderr)
	}
	code, stdout, stderr := exec(t, dbArgs("db", "apply", "-f", file, "--confirm", "production")...)
	if code != int(output.ExitConflict) || !committed || !strings.Contains(stdout, "total is 11 now") {
		t.Errorf("conflicting commit = %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestDBPolicySetMerges(t *testing.T) {
	setup(t)
	path := "/api/v1/projects/shop/data-policy"
	api := newFakeAPI(t, map[string]http.HandlerFunc{
		"GET " + path: jsonReply(apitypes.DataPolicy{Scope: apitypes.PolicyProject, Rules: []apitypes.DataPolicyRule{
			{Subject: apitypes.SubjectTokens, Kind: apitypes.KindProduction, Level: apitypes.LevelSchema, Sensitive: apitypes.SensitiveMasked},
			{Subject: apitypes.SubjectDeployUsers, Kind: apitypes.KindProduction, Level: apitypes.LevelRead, Sensitive: apitypes.SensitiveMasked},
		}}),
		"PUT " + path: jsonReply(apitypes.DataPolicy{Scope: apitypes.PolicyProject}),
	})
	if code, _, stderr := exec(t, "db", "policy", "set", "-p", "shop", "tokens.non-production=propose,visible", "deploy.production=inherit"); code != 0 {
		t.Fatalf("policy set = %d %s", code, stderr)
	}
	var req apitypes.UpdateDataPolicyRequest
	_ = json.Unmarshal([]byte(api.bodies["PUT "+path]), &req)
	if len(req.Rules) != 2 || req.Rules[0].Subject != apitypes.SubjectTokens || req.Rules[0].Kind != apitypes.KindProduction ||
		req.Rules[1].Kind != apitypes.KindNonProduction || req.Rules[1].Level != apitypes.LevelPropose || req.Rules[1].Sensitive != apitypes.SensitiveVisible {
		t.Errorf("PUT rules = %+v", req.Rules)
	}
	if code, _, _ := exec(t, "db", "policy", "set", "admins.production=write"); code != int(output.ExitUsage) {
		t.Errorf("unknown subject = %d, want usage", code)
	}
}

func TestTokenDataFlags(t *testing.T) {
	setup(t)
	api := newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /api/v1/tokens": jsonReply([]apitypes.Token{{ID: 7, Data: &apitypes.DataTokenAccess{
			Production: apitypes.LevelNone, NonProduction: apitypes.LevelSchema, Sensitive: apitypes.SensitiveMasked}}}),
		"PATCH /api/v1/tokens/7": jsonReply(apitypes.Token{ID: 7}),
		"POST /api/v1/tokens":    jsonReply(apitypes.CreatedToken{}),
	})
	exec(t, "token", "update", "7", "--data-non-production", "read")
	if got := api.bodies["PATCH /api/v1/tokens/7"]; got != `{"data":{"production":"none","non_production":"read","sensitive":"masked"}}` {
		t.Errorf("update body = %s", got)
	}
	exec(t, "token", "update", "7", "--data-clear")
	if got := api.bodies["PATCH /api/v1/tokens/7"]; got != `{"data":null}` {
		t.Errorf("clear body = %s", got)
	}
	exec(t, "token", "create", "agent", "--scope", "deploy", "--data-production", "propose")
	if got := api.bodies["POST /api/v1/tokens"]; !strings.Contains(got, `"data":{"production":"propose","non_production":"none","sensitive":"masked"}`) {
		t.Errorf("create body = %s", got)
	}
	if code, _, _ := exec(t, "token", "update", "7", "--data-production", "all"); code != int(output.ExitUsage) {
		t.Errorf("invalid level = %d, want usage", code)
	}
}
