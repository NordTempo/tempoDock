//go:build integration

package tbhttp

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// TestDataPanelIntegration drives the data panel through tempoBase's HTTP
// API against a real Postgres in Docker (a container with a per-run name,
// removed afterwards): test before save, setup of tempoBase's roles,
// sensitive columns masked for a viewer and revealed for an admin, read-only
// SQL, an agent's proposal approved by an admin, and a production commit
// that needs the connection's name. Run with
// `go test -tags integration ./internal/tempobase/tbhttp`.
func TestDataPanelIntegration(t *testing.T) {
	addr := startPostgres(t)
	url := "postgres://postgres:owner-pw@" + addr + "/postgres?sslmode=disable"

	srv, _, code := testServer(t)
	admin := &apiClient{t: t, srv: srv}
	admin.must(http.StatusOK, http.MethodPost, "/setup", tbtypes.SetupRequest{Code: code, Email: "admin@example.com", Name: "Admin", Password: "correct horse battery"}, nil)

	var tested tbtypes.TestConnectionResult
	admin.must(http.StatusOK, http.MethodPost, "/connections/test", tbtypes.TestConnectionRequest{URL: url}, &tested)
	if !tested.OK || !tested.CanSetUp || !strings.HasPrefix(tested.ServerVersion, "17") {
		t.Fatalf("test: %+v", tested)
	}
	admin.must(http.StatusOK, http.MethodPost, "/connections/test", tbtypes.TestConnectionRequest{URL: strings.Replace(url, "owner-pw", "wrong", 1)}, &tested)
	if tested.OK || tested.Error == "" || strings.Contains(tested.Error, "wrong") {
		t.Fatalf("test with a wrong password: %+v", tested)
	}
	admin.must(http.StatusOK, http.MethodPost, "/connections", tbtypes.CreateConnectionRequest{Name: "shop", URL: url}, nil)

	data := "/connections/shop/data"
	var info apitypes.DataInfo
	admin.must(http.StatusOK, http.MethodGet, data, nil, &info)
	if info.Status != apitypes.DataSetupNeeded {
		t.Fatalf("before setup: %+v", info)
	}
	admin.must(http.StatusOK, http.MethodPost, data+"/setup", nil, &info)
	if info.Status != apitypes.DataReady {
		t.Fatalf("after setup: %+v", info)
	}
	var schema apitypes.DataSchema
	admin.must(http.StatusOK, http.MethodGet, data+"/schema", nil, &schema)
	if !strings.Contains(fmt.Sprint(schema), "customers") {
		t.Fatalf("schema lacks customers: %+v", schema)
	}
	admin.must(http.StatusOK, http.MethodPut, data+"/sensitive-columns", apitypes.SetSensitiveColumnsRequest{
		Columns: []apitypes.DataColumnRef{{Schema: "public", Table: "customers", Column: "email"}},
	}, nil)

	// A viewer reads with email masked and can't reveal it, not even
	// through SQL.
	var link tbtypes.Link
	admin.must(http.StatusOK, http.MethodPost, "/invites", tbtypes.CreateInviteRequest{Email: "viewer@example.com", Role: tbtypes.RoleViewer}, &link)
	_, linkCode, _ := strings.Cut(link.URL, "#")
	viewer := &apiClient{t: t, srv: srv}
	viewer.must(http.StatusOK, http.MethodPost, "/links/"+linkCode, tbtypes.UseLinkRequest{Name: "Vera", Password: "correct horse battery"}, nil)
	rows := data + "/tables/public.customers/rows"
	var page apitypes.DataPage
	viewer.must(http.StatusOK, http.MethodGet, rows, nil, &page)
	if len(page.Rows) != 3 || !strings.Contains(string(page.Rows[0]), `{"masked":true}`) || strings.Contains(fmt.Sprint(page.Rows), "@shop.test") {
		t.Fatalf("viewer page: %s", page.Rows)
	}
	viewer.must(http.StatusForbidden, http.MethodGet, rows+"?reveal=true", nil, nil)
	if _, body := viewer.call(http.MethodPost, data+"/query", apitypes.DataQueryRequest{SQL: "SELECT email FROM customers"}, nil); strings.Contains(string(body), "@shop.test") {
		t.Fatalf("viewer selected a sensitive column: %s", body)
	}
	_, body := viewer.call(http.MethodPost, data+"/query", apitypes.DataQueryRequest{SQL: "SELECT count(*) FROM customers"}, nil)
	if !strings.Contains(string(body), `"rows":[["3"]]`) && !strings.Contains(string(body), `"rows":[[3]]`) {
		t.Fatalf("viewer query: %s", body)
	}
	if _, body := viewer.call(http.MethodPost, data+"/query", apitypes.DataQueryRequest{SQL: "DELETE FROM customers"}, nil); !strings.Contains(string(body), "only read statements") {
		t.Fatalf("a write through the SQL editor: %s", body)
	}
	admin.must(http.StatusOK, http.MethodGet, rows+"?reveal=true", nil, &page)
	if !strings.Contains(string(page.Rows[0]), "ada@shop.test") {
		t.Fatalf("admin reveal: %s", page.Rows[0])
	}

	// An agent proposes; an admin approves, and the change is in.
	var created tbtypes.CreatedToken
	admin.must(http.StatusOK, http.MethodPost, "/tokens", tbtypes.CreateTokenRequest{
		Name: "agent", Connections: []string{"shop"},
		Data: &apitypes.DataTokenAccess{Production: apitypes.LevelPropose, NonProduction: apitypes.LevelPropose, Sensitive: apitypes.SensitiveMasked},
	}, &created)
	agent := &apiClient{t: t, srv: srv, bearer: created.Secret}
	rename := func(from, to string) apitypes.DataChangesetRequest {
		return apitypes.DataChangesetRequest{Message: "fix the name", Changes: []apitypes.DataChange{{
			Op: apitypes.DataChangeUpdate, Table: "public.customers", Key: map[string]string{"id": "1"},
			Old: map[string]*string{"name": &from}, New: map[string]*string{"name": &to},
		}}}
	}
	var cs apitypes.DataChangeset
	agent.must(http.StatusCreated, http.MethodPost, data+"/changesets", rename("Ada", "Ada L."), &cs)
	if cs.Status != apitypes.ChangesetPending {
		t.Fatalf("proposal: %+v", cs)
	}
	agent.must(http.StatusForbidden, http.MethodPost, fmt.Sprintf("%s/changesets/%d/commit", data, cs.ID), nil, nil)
	admin.must(http.StatusOK, http.MethodPost, fmt.Sprintf("%s/changesets/%d/commit", data, cs.ID), nil, &cs)
	if cs.Status != apitypes.ChangesetCommitted {
		t.Fatalf("approved: %+v", cs)
	}
	admin.must(http.StatusOK, http.MethodGet, rows+"?limit=1", nil, &page)
	if !strings.Contains(string(page.Rows[0]), "Ada L.") {
		t.Fatalf("after commit: %s", page.Rows[0])
	}

	// On production, a commit confirms the connection's name.
	production := true
	admin.must(http.StatusOK, http.MethodPatch, "/connections/shop", tbtypes.UpdateConnectionRequest{Production: &production}, nil)
	admin.must(http.StatusCreated, http.MethodPost, data+"/changesets", rename("Ada L.", "Ada"), &cs)
	commit := fmt.Sprintf("%s/changesets/%d/commit", data, cs.ID)
	admin.must(http.StatusBadRequest, http.MethodPost, commit, apitypes.DataCommitRequest{}, nil)
	admin.must(http.StatusOK, http.MethodPost, commit, apitypes.DataCommitRequest{Confirm: "shop"}, &cs)
	if cs.Status != apitypes.ChangesetCommitted {
		t.Fatalf("production commit: %+v", cs)
	}

	// Everything above is in the audit log, the SQL in its details.
	var events []tbtypes.AuditEvent
	admin.must(http.StatusOK, http.MethodGet, "/audit?connection=shop", nil, &events)
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Action] = true
	}
	for _, action := range []string{"data.setup", "data.query", "data.browse", "data.reveal", "data.propose", "data.approve", "data.commit"} {
		if !seen[action] {
			t.Errorf("audit lacks %s: %+v", action, seen)
		}
	}
}

// startPostgres runs postgres:17-alpine on a random loopback port with a
// small seeded table, and returns its address once it accepts logins.
func startPostgres(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("tempobase-it-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_PASSWORD=owner-pw", "-p", "127.0.0.1::5432", "postgres:17-alpine").CombinedOutput()
	if err != nil {
		t.Fatalf("start postgres: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	out, err = exec.Command("docker", "port", name, "5432/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	for {
		conn, err := pgx.Connect(ctx, "postgres://postgres:owner-pw@"+addr+"/postgres?sslmode=disable")
		if err == nil {
			_, err = conn.Exec(ctx, `CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL, email text);
				INSERT INTO customers VALUES (1, 'Ada', 'ada@shop.test'), (2, 'Grace', 'grace@shop.test'), (3, 'Edsger', 'edsger@shop.test');
				ANALYZE`)
			_ = conn.Close(ctx)
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			return addr
		}
		select {
		case <-ctx.Done():
			t.Fatalf("postgres didn't start: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
