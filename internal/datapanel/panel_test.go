package datapanel

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/pg"
	"github.com/NordTempo/tempoDock/internal/secrets"
)

// fakeHost is an in-memory Host for one database; its handle is the
// database's name.
type fakeHost struct {
	mu         sync.Mutex
	notRunning bool
	access     *Access
	sensitive  []apitypes.DataColumnRef
	keys       []string
	changesets []Changeset
	audits     []AuditEntry
}

var _ Host[string] = (*fakeHost)(nil)

func (h *fakeHost) Connect(context.Context, string) (Conn, error) {
	if h.notRunning {
		return Conn{}, ErrNotRunning
	}
	dial := func(context.Context) (net.Conn, error) { return nil, data.ErrUnreachable }
	return Conn{Dial: dial, Database: "app", Owner: data.Credentials{User: "postgres", Password: "owner"}}, nil
}

func (h *fakeHost) Access(context.Context, int64) (*Access, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.access == nil {
		return nil, nil
	}
	acc := *h.access
	return &acc, nil
}

func (h *fakeHost) PutAccess(_ context.Context, _ int64, acc Access) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.access = &acc
	return nil
}

func (h *fakeHost) SetGrantsHash(_ context.Context, _ int64, hash string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.access.GrantsSyncedHash = hash
	return nil
}

func (h *fakeHost) SensitiveColumns(context.Context, int64) ([]apitypes.DataColumnRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sensitive, nil
}

func (h *fakeHost) ReplaceSensitiveColumns(_ context.Context, _ int64, cols []apitypes.DataColumnRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sensitive = cols
	return nil
}

func (h *fakeHost) SensitiveKeys(context.Context, int64) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.keys, nil
}

func (h *fakeHost) ReplaceSensitiveKeys(_ context.Context, _ int64, patterns []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keys = patterns
	return nil
}

// SavedQueryOwner: users own saved queries, tokens don't.
func (h *fakeHost) SavedQueryOwner(_ context.Context, a Actor) (int64, error) { return a.UserID, nil }

func (h *fakeHost) SavedQueries(context.Context, int64, int64) ([]apitypes.DataSavedQuery, error) {
	return []apitypes.DataSavedQuery{}, nil
}

func (h *fakeHost) SaveQuery(context.Context, int64, int64, int64, string, string, time.Time) (apitypes.DataSavedQuery, error) {
	return apitypes.DataSavedQuery{}, nil
}

func (h *fakeHost) DeleteSavedQuery(context.Context, int64, int64, int64) (bool, error) {
	return false, nil
}

func (h *fakeHost) CreateChangeset(_ context.Context, cs Changeset) (Changeset, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cs.ID = int64(len(h.changesets) + 1)
	h.changesets = append(h.changesets, cs)
	return cs, nil
}

func (h *fakeHost) Changeset(_ context.Context, _, id int64) (Changeset, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id < 1 || id > int64(len(h.changesets)) {
		return Changeset{}, ErrNotFound
	}
	return h.changesets[id-1], nil
}

func (h *fakeHost) Changesets(context.Context, int64, bool, int) ([]Changeset, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Changeset(nil), h.changesets...), nil
}

func (h *fakeHost) DecideChangeset(_ context.Context, d Decision) (Changeset, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cs := &h.changesets[d.ID-1]
	if cs.Status != apitypes.ChangesetPending {
		return Changeset{}, ErrNotFound
	}
	at := d.DecidedAt
	cs.Status, cs.DecidedAt, cs.DecidedBy, cs.RowsAffected, cs.Conflicts, cs.Reason = d.Status, &at, d.DecidedBy, d.RowsAffected, d.Conflicts, d.Reason
	return *cs, nil
}

func (h *fakeHost) ExpireChangesets(context.Context, time.Time) error { return nil }

func (h *fakeHost) Audit(_ context.Context, _ Actor, _ string, e AuditEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audits = append(h.audits, e)
}

func (h *fakeHost) QueryHistory(context.Context, Actor, string, int64, int) ([]apitypes.DataHistoryEntry, error) {
	return nil, nil
}

// actions lists the audited actions in order.
func (h *fakeHost) actions() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.audits {
		out = append(out, e.Action)
	}
	return strings.Join(out, ",")
}

// fakeEngine implements the engine calls the tests reach; Redact is
// Postgres's. Setup records the passwords it was given.
type fakeEngine struct {
	data.Engine
	pg     *pg.Engine
	reader string
}

func (e *fakeEngine) Setup(_ context.Context, _ data.Target, _, reader, _ data.Credentials) (data.ServerInfo, error) {
	e.reader = reader.Password
	return data.ServerInfo{Version: "17.2"}, nil
}

func (e *fakeEngine) Status(context.Context, data.Target) (data.ServerInfo, error) {
	return data.ServerInfo{Version: "17.2"}, nil
}

func (e *fakeEngine) SyncGrants(context.Context, data.Target, data.Credentials, []apitypes.DataColumnRef) (string, error) {
	return "synced", nil
}

func (e *fakeEngine) Schema(context.Context, data.Target, data.Call, []apitypes.DataColumnRef) (apitypes.DataSchema, error) {
	return apitypes.DataSchema{}, nil
}

func (e *fakeEngine) Render(context.Context, data.Target, []apitypes.DataChange) (string, error) {
	return "UPDATE t SET v = 'x' WHERE id = '1'", nil
}

func (e *fakeEngine) Commit(_ context.Context, _ data.Target, _ data.Call, changes []apitypes.DataChange) (data.CommitResult, error) {
	return data.CommitResult{SQL: "BEGIN; UPDATE ...; COMMIT;", RowsAffected: make([]int64, len(changes))}, nil
}

func (e *fakeEngine) Redact(sql string) string { return e.pg.Redact(sql) }

func (e *fakeEngine) Kind() apitypes.DataEngine { return apitypes.EnginePostgres }

func (e *fakeEngine) Page(context.Context, data.Target, data.Call, apitypes.DataPageRequest) (apitypes.DataPage, error) {
	return apitypes.DataPage{}, nil
}

func (e *fakeEngine) References(context.Context, data.Target, data.Call, apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	return nil, nil
}

func newTestPanel(t *testing.T) (*Panel[string], *fakeHost, *fakeEngine) {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	h, eng := &fakeHost{}, &fakeEngine{pg: pg.New(pg.Options{})}
	return New(Options[string]{Host: h, Engines: []data.Engine{eng}, Box: box, Words: Words{Product: "Test", SetupHint: "set it up"}}), h, eng
}

func testDB(perm apitypes.DataPermission) *Database[string] {
	return &Database[string]{ID: 1, Name: "orders", Engine: apitypes.EnginePostgres, ConfirmName: "orders", Perm: perm, Host: "orders"}
}

var (
	adminPerm = apitypes.DataPermission{Level: apitypes.LevelWrite, Sensitive: visible, Approve: true, Manage: true}
	agentPerm = apitypes.DataPermission{Level: apitypes.LevelPropose, Sensitive: masked}
	admin     = Actor{Kind: "user", Name: "admin@x", UserID: 1}
	agent     = Actor{Kind: "token", Name: "bot", TokenID: 9}
)

// TestPanelSetup: setup needs Manage, stores the roles' passwords sealed
// and is audited; until then the panel says how to set it up.
func TestPanelSetup(t *testing.T) {
	p, h, eng := newTestPanel(t)
	ctx := context.Background()
	if info, err := p.Info(ctx, admin, testDB(adminPerm)); err != nil || info.Status != apitypes.DataSetupNeeded {
		t.Fatalf("before setup: %+v, %v", info, err)
	}
	if _, err := p.Schema(ctx, admin, testDB(adminPerm)); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "set it up") {
		t.Fatalf("schema before setup: %v", err)
	}
	if _, err := p.Setup(ctx, agent, testDB(agentPerm)); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("setup without manage: %v", err)
	}
	info, err := p.Setup(ctx, admin, testDB(adminPerm))
	if err != nil || info.Status != apitypes.DataReady || info.SetUpBy != "admin@x" {
		t.Fatalf("setup: %+v, %v", info, err)
	}
	if eng.reader == "" || bytes.Contains(h.access.ReaderSecret, []byte(eng.reader)) || h.access.GrantsSyncedHash != "synced" {
		t.Fatalf("access record: %+v", h.access)
	}
	if got := h.actions(); got != apitypes.AuditDataSetup || !strings.HasPrefix(h.audits[0].Summary, "set up data access on Postgres 17.2: ") {
		t.Fatalf("audited %s: %+v", got, h.audits)
	}
	h.notRunning = true
	if info, err := p.Info(ctx, admin, testDB(adminPerm)); err != nil || info.Status != apitypes.DataNotRunning {
		t.Fatalf("not running: %+v, %v", info, err)
	}
}

// TestPanelChangesets: an agent proposes, can't commit, an approver
// commits with the confirm name, and a decided changeset can't be
// decided again.
func TestPanelChangesets(t *testing.T) {
	p, h, _ := newTestPanel(t)
	ctx := context.Background()
	if _, err := p.Setup(ctx, admin, testDB(adminPerm)); err != nil {
		t.Fatal(err)
	}
	v := "x"
	req := apitypes.DataChangesetRequest{Changes: []apitypes.DataChange{{Op: apitypes.DataChangeUpdate, Table: "public.t", Key: map[string]string{"id": "1"}, New: map[string]*string{"v": &v}}}}
	cs, err := p.CreateChangeset(ctx, agent, testDB(agentPerm), req)
	if err != nil || cs.Status != apitypes.ChangesetPending {
		t.Fatalf("propose: %+v, %v", cs, err)
	}
	if _, err := p.Commit(ctx, agent, testDB(agentPerm), cs.ID, apitypes.DataCommitRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("agent committed its proposal: %v", err)
	}
	prod := adminPerm
	prod.ConfirmCommit = true
	if _, err := p.Commit(ctx, admin, testDB(prod), cs.ID, apitypes.DataCommitRequest{Confirm: "wrong"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("commit without the confirm name: %v", err)
	}
	done, err := p.Commit(ctx, admin, testDB(prod), cs.ID, apitypes.DataCommitRequest{Confirm: "orders"})
	if err != nil || done.Status != apitypes.ChangesetCommitted || done.DecidedBy != "admin@x" {
		t.Fatalf("commit: %+v, %v", done, err)
	}
	if _, err := p.Reject(ctx, admin, testDB(adminPerm), cs.ID, apitypes.DataRejectRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("rejected a committed changeset: %v", err)
	}
	if got := h.actions(); got != "data.setup,data.propose,data.commit,data.approve" {
		t.Fatalf("audited %s", got)
	}
	if d := h.audits[2].Detail; d == nil || d.Outcome != apitypes.OutcomeOK || d.Rows != 0 {
		t.Fatalf("commit detail: %+v", d)
	}
	if _, err := p.SavedQueries(ctx, agent, testDB(agentPerm)); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("token without a user listed saved queries: %v", err)
	}
}

// TestChangesetMasked: a changeset touching a sensitive column shows
// masked callers no value of it, in its changes, conflicts or SQL.
func TestChangesetMasked(t *testing.T) {
	p, _, _ := newTestPanel(t)
	old, cur, name := "old@x.io", "cur@x.io", "a"
	newEmail, newName := "new@x.io", "b"
	cs := Changeset{
		ID: 1, Status: apitypes.ChangesetConflict,
		Changes: []apitypes.DataChange{{Op: apitypes.DataChangeUpdate, Table: "public.customers", Key: map[string]string{"id": "1"},
			Old: map[string]*string{"email": &old, "name": &name}, New: map[string]*string{"email": &newEmail, "name": &newName}}},
		Conflicts: []apitypes.DataConflict{{Change: 0, Reason: "changed", Current: map[string]*string{"email": &cur, "name": &name}, Message: "m"}},
		SQL:       "UPDATE customers SET email = 'new@x.io', name = 'b' WHERE id = '1' AND email = 'old@x.io' AND name = 'a'",
	}
	tg := &target[string]{db: testDB(apitypes.DataPermission{Sensitive: masked}), sensitive: []apitypes.DataColumnRef{{Schema: "public", Table: "customers", Column: "email"}}}
	out := p.changesetFor(tg, cs)
	ch := out.Changes[0]
	if _, ok := ch.New["email"]; ok || len(ch.Old) != 1 || *ch.New["name"] != "b" || len(out.Conflicts[0].Current) != 1 {
		t.Fatalf("masked changes: %+v %+v", ch, out.Conflicts)
	}
	if strings.Contains(out.SQL, "@") || !strings.HasPrefix(out.SQL, "UPDATE customers SET email = ?") {
		t.Fatalf("masked SQL: %s", out.SQL)
	}
	if _, ok := cs.Changes[0].New["email"]; !ok {
		t.Fatal("masking changed the host's changeset")
	}
	tg.db.Perm.Sensitive = visible
	if out := p.changesetFor(tg, cs); out.SQL != cs.SQL || *out.Changes[0].New["email"] != "new@x.io" {
		t.Fatalf("visible: %+v", out)
	}
}

func TestBrowseThrottle(t *testing.T) {
	var b browseLog
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const browse = apitypes.AuditDataBrowse
	k := browseKey{browse, "user:1", 1, "public.orders"}
	if !b.due(k, at) || b.due(k, at.Add(time.Minute)) || !b.due(browseKey{browse, "user:1", 1, "public.items"}, at) || !b.due(browseKey{browse, "user:2", 1, "public.orders"}, at) {
		t.Fatal("first browse per actor and table is audited, repeats are not")
	}
	if !b.due(browseKey{apitypes.AuditDataReveal, "user:1", 1, "public.orders"}, at) {
		t.Fatal("a reveal is throttled apart from browsing")
	}
	if !b.due(k, at.Add(browseInterval)) {
		t.Fatal("audited again after the interval")
	}
	for i := range browseLogMax + 10 {
		b.due(browseKey{browse, "user:1", int64(i), "t"}, at.Add(time.Duration(i)*time.Hour))
	}
	if len(b.last) > browseLogMax {
		t.Fatalf("throttle holds %d entries", len(b.last))
	}
}

func TestCutSQL(t *testing.T) {
	long := strings.Repeat("é", apitypes.AuditSQLMaxBytes)
	got := cutSQL(long)
	if len(got) > apitypes.AuditSQLMaxBytes || !strings.HasPrefix(long, got) || len(got) < apitypes.AuditSQLMaxBytes-1 {
		t.Fatalf("cut to %d bytes", len(got))
	}
	if sqlSummary("SELECT *\n  FROM orders") != "SELECT * FROM orders" {
		t.Fatal(sqlSummary("SELECT *\n  FROM orders"))
	}
}

// A bare table name resolves to public's table, else the one schema that
// has it; in several, the caller names the schema.
func TestQualifyTable(t *testing.T) {
	tables := []apitypes.DataTable{
		{Schema: "public", Name: "users"}, {Schema: "audit", Name: "users"},
		{Schema: "shop", Name: "orders"},
		{Schema: "a", Name: "items"}, {Schema: "b", Name: "items"},
	}
	for name, want := range map[string]string{"users": "public.users", "orders": "shop.orders", "missing": "missing", "b.items": "b.items"} {
		if got, err := QualifyTable(tables, name); err != nil || got != want {
			t.Errorf("QualifyTable(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := QualifyTable(tables, "items"); err == nil || !strings.Contains(err.Error(), "a.items, b.items") {
		t.Errorf("an ambiguous name: %v", err)
	}
}
