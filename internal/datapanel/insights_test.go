package datapanel

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// insightsEngine is fakeEngine with insights: the monitor logs in only
// after EnsureMonitor, and every view comes back with one text.
type insightsEngine struct {
	*fakeEngine
	monitor bool
	views   []apitypes.DataInsightView
}

func (e *insightsEngine) Insights(_ context.Context, _ data.Target, _ data.Call, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	if !e.monitor {
		return apitypes.DataInsights{}, data.ErrNotSetUp
	}
	e.views = req.Views
	return apitypes.DataInsights{
		Sessions: []apitypes.DataSession{{ID: "1.1", Text: "SELECT * FROM users WHERE email = 'a@b.io'"}},
		Queries:  []apitypes.DataStatement{{ID: "9", Text: "ALTER ROLE app PASSWORD 'hunter2'"}},
	}, nil
}

func (e *insightsEngine) EnsureMonitor(context.Context, data.Target, data.Credentials) error {
	e.monitor = true
	return nil
}

// TestInsightsViewsAndTexts: a schema-level caller gets the views without
// texts and is told what the others need; asking only for those is
// forbidden. Texts are redacted for masked callers and, when they carry a
// password, for everyone. The monitor is created on first use, and a look
// at texts is audited once per throttle window.
func TestInsightsViewsAndTexts(t *testing.T) {
	p, h, base := newTestPanel(t)
	eng := &insightsEngine{fakeEngine: base}
	p.engines[apitypes.EnginePostgres] = eng
	ctx := context.Background()
	if _, err := p.Setup(ctx, admin, testDB(adminPerm)); err != nil {
		t.Fatal(err)
	}

	schemaOnly := apitypes.DataPermission{Level: apitypes.LevelSchema, Sensitive: masked}
	ins, err := p.Insights(ctx, agent, testDB(schemaOnly), apitypes.DataInsightsRequest{
		Views: []apitypes.DataInsightView{apitypes.InsightSessions, apitypes.InsightOverview},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(eng.views, []apitypes.DataInsightView{apitypes.InsightOverview}) {
		t.Errorf("engine read %v for a schema caller", eng.views)
	}
	if len(ins.Unavailable) != 1 || ins.Unavailable[0].View != apitypes.InsightSessions || !strings.Contains(ins.Unavailable[0].Reason, "level read") {
		t.Errorf("unavailable: %+v", ins.Unavailable)
	}
	if _, err := p.Insights(ctx, agent, testDB(schemaOnly), apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightQueries}}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("queries at level schema: %v", err)
	}

	readMasked := apitypes.DataPermission{Level: apitypes.LevelRead, Sensitive: masked}
	ins, err = p.Insights(ctx, agent, testDB(readMasked), apitypes.DataInsightsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(eng.views, apitypes.InsightViewsOf(apitypes.EnginePostgres)) {
		t.Errorf("default views for a reader: %v", eng.views)
	}
	if _, err := p.Insights(ctx, agent, testDB(readMasked), apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightKeys}}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("Redis' keys view of Postgres: %v", err)
	}
	if s := ins.Sessions[0].Text; strings.Contains(s, "a@b.io") {
		t.Errorf("masked caller sees a value: %s", s)
	}
	ins, err = p.Insights(ctx, admin, testDB(adminPerm), apitypes.DataInsightsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if s := ins.Sessions[0].Text; !strings.Contains(s, "a@b.io") {
		t.Errorf("visible caller gets a redacted text: %s", s)
	}
	if q := ins.Queries[0].Text; strings.Contains(q, "hunter2") {
		t.Errorf("a password reached an admin: %s", q)
	}

	if got := strings.Count(h.actions(), apitypes.AuditDataInsights); got != 2 {
		t.Errorf("%d data.insights events, want one per caller: %s", got, h.actions())
	}
	if _, err := p.Insights(ctx, admin, testDB(adminPerm), apitypes.DataInsightsRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(h.actions(), apitypes.AuditDataInsights); got != 2 {
		t.Errorf("a second look within the window was audited again: %s", h.actions())
	}
}

// cancelEngine is fakeEngine with Cancel: session "1.1" is running.
type cancelEngine struct{ *fakeEngine }

func (cancelEngine) Cancel(_ context.Context, _ data.Target, _ data.Credentials, session string, _ *time.Time, _ bool) (data.CancelResult, error) {
	if session != "1.1" {
		return data.CancelResult{}, nil
	}
	return data.CancelResult{Found: true, User: "postgres", Application: "worker", Text: "UPDATE t SET v = 'secret'", Running: time.Minute}, nil
}

// TestCancelSession: cancelling needs level write, ending a session on
// production confirms with its name, a session that moved on is a
// conflict, and what happened is audited without the statement's values
// in the summary.
func TestCancelSession(t *testing.T) {
	p, h, base := newTestPanel(t)
	p.engines[apitypes.EnginePostgres] = cancelEngine{base}
	ctx := context.Background()

	reader := apitypes.DataPermission{Level: apitypes.LevelRead, Sensitive: masked}
	if _, err := p.CancelSession(ctx, agent, testDB(reader), "1.1", apitypes.DataCancelRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("cancel at level read: %v", err)
	}
	prod := adminPerm
	prod.ConfirmCommit = true
	if _, err := p.CancelSession(ctx, admin, testDB(prod), "1.1", apitypes.DataCancelRequest{Terminate: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("ending a production session without confirm: %v", err)
	}
	if _, err := p.CancelSession(ctx, admin, testDB(prod), "2.2", apitypes.DataCancelRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a finished query: %v", err)
	}
	res, err := p.CancelSession(ctx, admin, testDB(prod), "1.1", apitypes.DataCancelRequest{Terminate: true, Confirm: "orders"})
	if err != nil || !res.Terminated || res.Application != "worker" || res.RunningMs != 60_000 {
		t.Fatalf("end session: %+v %v", res, err)
	}
	h.mu.Lock()
	last := h.audits[len(h.audits)-1]
	h.mu.Unlock()
	if last.Action != apitypes.AuditDataTerminate || strings.Contains(last.Summary, "secret") || last.Detail == nil || !strings.Contains(last.Detail.SQL, "secret") {
		t.Fatalf("audit: %+v %+v", last, last.Detail)
	}
}
