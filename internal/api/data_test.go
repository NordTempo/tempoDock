package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// fakeData stands in for core's database panel operations. Methods a test
// doesn't set panic through the nil embedded interface.
type fakeData struct {
	dataCore
	rows     func(apitypes.DataPageRequest) (apitypes.DataPage, error)
	refs     func(apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error)
	query    func(apitypes.DataQueryRequest) (core.Stream[apitypes.DataQueryFrame], error)
	info     func(core.Actor) (apitypes.DataInfo, error)
	create   func(apitypes.DataChangesetRequest) (apitypes.DataChangeset, error)
	token    func(int64, apitypes.UpdateTokenRequest) (apitypes.Token, error)
	policyAt func(core.EnvRef) (apitypes.DataPolicy, error)
}

func (f *fakeData) DataInfo(_ context.Context, a core.Actor, _ core.ServiceRef) (apitypes.DataInfo, error) {
	return f.info(a)
}

func (f *fakeData) DataRows(_ context.Context, _ core.Actor, _ core.ServiceRef, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	return f.rows(req)
}

func (f *fakeData) DataReferences(_ context.Context, _ core.Actor, _ core.ServiceRef, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	return f.refs(req)
}

func (f *fakeData) DataQuery(_ context.Context, _ core.Actor, _ core.ServiceRef, req apitypes.DataQueryRequest) (core.Stream[apitypes.DataQueryFrame], error) {
	return f.query(req)
}

func (f *fakeData) CreateDataChangeset(_ context.Context, _ core.Actor, _ core.ServiceRef, req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error) {
	return f.create(req)
}

func (f *fakeData) UpdateToken(_ context.Context, _ core.Actor, id int64, req apitypes.UpdateTokenRequest) (apitypes.Token, error) {
	return f.token(id, req)
}

func (f *fakeData) GetDataPolicy(_ context.Context, _ core.Actor, ref core.EnvRef) (apitypes.DataPolicy, error) {
	return f.policyAt(ref)
}

// withData points the fixture at a server whose data endpoints call fd;
// auth still goes through the real core.
func (f *fixture) withData(fd *fakeData) {
	mux := http.NewServeMux()
	newAPI(mux, &Server{core: f.core, opts: Options{Logger: slog.New(slog.DiscardHandler)}, data: fd})
	srv := httptest.NewServer(guard(mux))
	f.t.Cleanup(srv.Close)
	f.srv = srv
}

const shopData = shopProd + "/services/web/data"

func TestDataRowsParameters(t *testing.T) {
	f := newFixture(t)
	var got apitypes.DataPageRequest
	f.withData(&fakeData{rows: func(req apitypes.DataPageRequest) (apitypes.DataPage, error) {
		got = req
		return apitypes.DataPage{Columns: []apitypes.DataResultColumn{{Name: "id", TypeName: "int4"}}, Rows: []apitypes.DataRow{json.RawMessage(`[1]`)}, Next: "c2"}, nil
	}})

	filter := `[{"column":"status","op":"eq","value":"paid"}]`
	res, body := f.do("read", "GET", shopData+"/tables/public.orders/rows?order=-total&after=c1&limit=50&count=true&filter="+url.QueryEscape(filter), nil)
	if res.StatusCode != 200 {
		t.Fatalf("rows = %d %s", res.StatusCode, body)
	}
	want := apitypes.DataPageRequest{Table: "public.orders", Order: "-total", After: "c1", Limit: 50, Count: true,
		Filters: []apitypes.DataFilter{{Column: "status", Op: apitypes.FilterEq, Value: "paid"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("core got %+v, want %+v", got, want)
	}
	if !strings.Contains(string(body), `"rows":[[1]]`) || !strings.Contains(string(body), `"next":"c2"`) {
		t.Errorf("rows body = %s", body)
	}

	for _, q := range []string{"filter=" + url.QueryEscape("{"), "limit=1001"} {
		res, body := f.do("read", "GET", shopData+"/tables/public.orders/rows?"+q, nil)
		if res.StatusCode != 400 && res.StatusCode != 422 || !strings.Contains(string(body), `"code":"validation"`) {
			t.Errorf("%s = %d %s, want a validation error", q, res.StatusCode, body)
		}
	}
}

func TestDataReferencesKey(t *testing.T) {
	f := newFixture(t)
	var got apitypes.DataReferencesRequest
	f.withData(&fakeData{refs: func(req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
		got = req
		return []apitypes.DataReferences{}, nil
	}})
	if res, body := f.do("read", "GET", shopData+"/tables/public.customers/references?limit=5&key="+url.QueryEscape(`{"id":"42"}`), nil); res.StatusCode != 200 {
		t.Fatalf("references = %d %s", res.StatusCode, body)
	}
	if got.Table != "public.customers" || got.Key["id"] != "42" || got.Limit != 5 {
		t.Errorf("core got %+v", got)
	}
	if res, _ := f.do("read", "GET", shopData+"/tables/public.customers/references?key=42", nil); res.StatusCode != 400 {
		t.Errorf("a key that isn't an object = %d, want 400", res.StatusCode)
	}
}

// Core errors keep their message and map to statuses the CLI turns into
// exit codes.
func TestDataErrorMapping(t *testing.T) {
	f := newFixture(t)
	var next error
	f.withData(&fakeData{info: func(a core.Actor) (apitypes.DataInfo, error) { return apitypes.DataInfo{}, next }})
	cases := []struct {
		err    error
		status int
	}{
		{&core.Error{Code: apitypes.CodeForbidden, Msg: "reading data needs level read; built-in default: role:deploy may not read production"}, 403},
		{&core.Error{Code: apitypes.CodeConflict, Msg: "changeset 3 is not pending"}, 409},
		{&core.Error{Code: apitypes.CodeServerOffline, Msg: "the database is not running"}, 503},
		{&core.Error{Code: apitypes.CodeNotFound, Msg: "web is not a database"}, 404},
		{core.ErrDataNotImplemented, 400},
	}
	for _, c := range cases {
		next = c.err
		res, body := f.do("read", "GET", shopData, nil)
		var p apitypes.Error
		_ = json.Unmarshal(body, &p)
		if res.StatusCode != c.status || p.Detail != c.err.Error() {
			t.Errorf("%v: %d %s, want %d with the message", c.err, res.StatusCode, body, c.status)
		}
	}
	if res, _ := f.do("", "GET", shopData, nil); res.StatusCode != 401 {
		t.Errorf("no credentials = %d, want 401", res.StatusCode)
	}
}

func TestDataChangesetStatus(t *testing.T) {
	f := newFixture(t)
	f.withData(&fakeData{create: func(req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error) {
		if req.DryRun {
			return apitypes.DataChangeset{Status: apitypes.ChangesetPreview}, nil
		}
		return apitypes.DataChangeset{ID: 7, Status: apitypes.ChangesetPending}, nil
	}})
	change := apitypes.DataChange{Op: apitypes.DataChangeDelete, Table: "public.orders", Key: map[string]string{"id": "1"}}
	for dry, want := range map[bool]int{true: 200, false: 201} {
		res, body := f.do("deploy", "POST", shopData+"/changesets", apitypes.DataChangesetRequest{Changes: []apitypes.DataChange{change}, DryRun: dry})
		if res.StatusCode != want {
			t.Errorf("dry_run %v = %d %s, want %d", dry, res.StatusCode, body, want)
		}
	}
}

func TestUpdateTokenDataNull(t *testing.T) {
	f := newFixture(t)
	var got []apitypes.UpdateTokenRequest
	f.withData(&fakeData{token: func(id int64, req apitypes.UpdateTokenRequest) (apitypes.Token, error) {
		got = append(got, req)
		return apitypes.Token{ID: id, Data: req.Data}, nil
	}})
	for _, body := range []string{`{"data":{"production":"none","non_production":"read","sensitive":"masked"}}`, `{"data":null}`} {
		res, b := f.doRaw("admin", "PATCH", "/api/v1/tokens/3", body)
		if res.StatusCode != 200 {
			t.Fatalf("%s = %d %s", body, res.StatusCode, b)
		}
	}
	if len(got) != 2 || got[0].Data == nil || got[0].Data.NonProduction != apitypes.LevelRead || got[1].Data != nil {
		t.Errorf("core got %+v", got)
	}
}

func TestDataPolicyScopes(t *testing.T) {
	f := newFixture(t)
	var got []core.EnvRef
	f.withData(&fakeData{policyAt: func(ref core.EnvRef) (apitypes.DataPolicy, error) {
		got = append(got, ref)
		return apitypes.DataPolicy{Rules: []apitypes.DataPolicyRule{}}, nil
	}})
	for _, p := range []string{"/api/v1/data-policy", "/api/v1/projects/shop/data-policy", shopProd + "/data-policy"} {
		if res, body := f.do("read", "GET", p, nil); res.StatusCode != 200 {
			t.Fatalf("%s = %d %s", p, res.StatusCode, body)
		}
	}
	want := []core.EnvRef{{}, {Project: "shop"}, {Project: "shop", Environment: "production"}}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("scopes = %+v, want %+v", got, want)
	}
}

// queryStream returns a stream that sends batches, then returns err.
func queryStream(err error, batches ...[]apitypes.DataQueryFrame) core.Stream[apitypes.DataQueryFrame] {
	return func(ctx context.Context, ready func(), send func(...apitypes.DataQueryFrame) error) error {
		for _, b := range batches {
			if err := send(b...); err != nil {
				return err
			}
		}
		return err
	}
}

func TestDataQueryNDJSON(t *testing.T) {
	f := newFixture(t)
	var next core.Stream[apitypes.DataQueryFrame]
	var got apitypes.DataQueryRequest
	f.withData(&fakeData{query: func(req apitypes.DataQueryRequest) (core.Stream[apitypes.DataQueryFrame], error) {
		got = req
		return next, nil
	}})
	header := apitypes.DataQueryFrame{Type: apitypes.FrameHeader, Columns: []apitypes.DataResultColumn{{Name: "n", TypeName: "int4"}}}
	rows := func(n string) apitypes.DataQueryFrame {
		return apitypes.DataQueryFrame{Type: apitypes.FrameRows, Rows: []apitypes.DataRow{json.RawMessage("[" + n + "]")}}
	}
	end := apitypes.DataQueryFrame{Type: apitypes.FrameEnd, RowCount: 2, Command: "SELECT 2"}

	t.Run("frames", func(t *testing.T) {
		next = queryStream(nil, []apitypes.DataQueryFrame{header, rows("1")}, []apitypes.DataQueryFrame{rows("2"), end})
		res, body := f.do("read", "POST", shopData+"/query", apitypes.DataQueryRequest{SQL: "select 1", Limit: 10, TimeoutSeconds: 5})
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/x-ndjson" {
			t.Fatalf("query = %d %s %s", res.StatusCode, res.Header.Get("Content-Type"), body)
		}
		if got.SQL != "select 1" || got.Limit != 10 || got.TimeoutSeconds != 5 {
			t.Errorf("core got %+v", got)
		}
		lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
		var types []string
		for _, l := range lines {
			var fr apitypes.DataQueryFrame
			if err := json.Unmarshal([]byte(l), &fr); err != nil {
				t.Fatalf("line %q is not a frame: %v", l, err)
			}
			types = append(types, string(fr.Type))
		}
		if strings.Join(types, ",") != "header,rows,rows,end" {
			t.Errorf("frames = %v", types)
		}
	})

	t.Run("error before the first frame is a problem", func(t *testing.T) {
		next = queryStream(&core.Error{Code: apitypes.CodeForbidden, Msg: "masked callers can't run SQL"})
		res, body := f.do("read", "POST", shopData+"/query", apitypes.DataQueryRequest{SQL: "select 1"})
		if res.StatusCode != 403 || !strings.Contains(string(body), "masked callers") {
			t.Errorf("query = %d %s", res.StatusCode, body)
		}
	})

	t.Run("error after frames ends with an error frame", func(t *testing.T) {
		next = queryStream(&core.Error{Code: apitypes.CodeServerOffline, Msg: "connection lost"}, []apitypes.DataQueryFrame{header})
		res, body := f.do("read", "POST", shopData+"/query", apitypes.DataQueryRequest{SQL: "select 1"})
		if res.StatusCode != 200 || !strings.HasSuffix(string(body), `{"type":"error","error":{"message":"connection lost"}}`+"\n") {
			t.Errorf("query = %d %s", res.StatusCode, body)
		}
	})

	t.Run("client disconnect cancels the stream", func(t *testing.T) {
		canceled := make(chan struct{})
		next = func(ctx context.Context, ready func(), send func(...apitypes.DataQueryFrame) error) error {
			if err := send(header); err != nil {
				return err
			}
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		}
		req, _ := http.NewRequest("POST", f.srv.URL+shopData+"/query", strings.NewReader(`{"sql":"select pg_sleep(60)"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+f.tokens["read"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		select {
		case <-canceled:
		case <-time.After(5 * time.Second):
			t.Fatal("closing the response did not cancel the query")
		}
	})

	t.Run("validation", func(t *testing.T) {
		res, body := f.do("read", "POST", shopData+"/query", apitypes.DataQueryRequest{SQL: "select 1", Limit: apitypes.DataQueryMaxLimit + 1})
		if res.StatusCode != 422 && res.StatusCode != 400 {
			t.Errorf("limit over the max = %d %s", res.StatusCode, body)
		}
	})
}

// doRaw sends body as is, for JSON a Go value can't express.
func (f *fixture) doRaw(token, method, path, body string) (result, []byte) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.tokens[token])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{StatusCode: resp.StatusCode, Header: resp.Header}, b
}
