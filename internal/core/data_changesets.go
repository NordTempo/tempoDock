package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Changesets (ADR 0016 section 5): row changes previewed, stored as
// pending, then committed in one transaction (by their author with level
// write, or approved by an admin) or rejected. Pending ones expire after
// apitypes.DataChangesetExpiry; nothing runs on a timer, they are expired
// lazily before they are read.

// decisions keeps two decisions on one changeset (two approvals, an
// approval and a rejection) from running at once in this daemon: a commit
// must not run twice against the database.
type decisions struct {
	mu  sync.Mutex
	ids map[int64]bool
}

// claim marks a changeset as being decided; false if it already is.
func (d *decisions) claim(id int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ids[id] {
		return false
	}
	if d.ids == nil {
		d.ids = map[int64]bool{}
	}
	d.ids[id] = true
	return true
}

func (d *decisions) done(id int64) {
	d.mu.Lock()
	delete(d.ids, id)
	d.mu.Unlock()
}

// toChangeset converts a stored changeset.
func toChangeset(cs store.Changeset, confirm bool) (apitypes.DataChangeset, error) {
	out := apitypes.DataChangeset{
		ID: cs.ID, Status: apitypes.DataChangesetStatus(cs.Status), Author: cs.Author, AuthorKind: cs.AuthorKind,
		Message: cs.Message, SQL: cs.Sql, ConfirmCommit: confirm, CreatedAt: cs.CreatedAt, ExpiresAt: &cs.ExpiresAt,
		DecidedAt: cs.DecidedAt, DecidedBy: cs.DecidedBy, Reason: cs.Reason,
	}
	if err := json.Unmarshal([]byte(cs.Changes), &out.Changes); err != nil {
		return out, fmt.Errorf("changeset %d changes: %w", cs.ID, err)
	}
	if cs.RowsAffected != "" {
		if err := json.Unmarshal([]byte(cs.RowsAffected), &out.RowsAffected); err != nil {
			return out, fmt.Errorf("changeset %d rows: %w", cs.ID, err)
		}
	}
	if cs.Conflicts != "" {
		if err := json.Unmarshal([]byte(cs.Conflicts), &out.Conflicts); err != nil {
			return out, fmt.Errorf("changeset %d conflicts: %w", cs.ID, err)
		}
	}
	return out, nil
}

// isAuthor reports whether the actor wrote the changeset.
func isAuthor(a Actor, cs store.Changeset) bool {
	switch {
	case a.UserID != 0:
		return cs.UserID != nil && *cs.UserID == a.UserID
	case a.TokenID != 0:
		return cs.TokenID != nil && *cs.TokenID == a.TokenID
	}
	return false
}

// checkSensitiveChanges refuses changes that read or write a column the
// caller sees masked.
func checkSensitiveChanges(dt *dbTarget, changes []apitypes.DataChange) error {
	if dt.perm.Sensitive == apitypes.SensitiveVisible {
		return nil
	}
	for _, ch := range changes {
		for _, col := range dt.sensitive {
			if ch.Table != col.Schema+"."+col.Table {
				continue
			}
			_, inKey := ch.Key[col.Column]
			_, inOld := ch.Old[col.Column]
			_, inNew := ch.New[col.Column]
			if inKey || inOld || inNew {
				return errf(apitypes.CodeForbidden, "%s.%s is sensitive and masked for you; leave it out of the changes", ch.Table, col.Column)
			}
		}
	}
	return nil
}

// summarizeChanges says what a changeset touches, e.g. "3 changes to
// public.orders".
func summarizeChanges(changes []apitypes.DataChange) string {
	var tables []string
	for _, ch := range changes {
		if !slices.Contains(tables, ch.Table) {
			tables = append(tables, ch.Table)
		}
	}
	noun := "changes"
	if len(changes) == 1 {
		noun = "change"
	}
	return fmt.Sprintf("%d %s to %s", len(changes), noun, strings.Join(tables, ", "))
}

// CreateDataChangeset previews (dry run) or stores a changeset as pending.
// Needs level propose. The changes are validated against the schema and
// rendered to the SQL a commit would run; nothing is written to the
// database. Stored changesets are audited as data.propose and expire
// after apitypes.DataChangesetExpiry.
func (c *Core) CreateDataChangeset(ctx context.Context, a Actor, ref ServiceRef, req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error) {
	switch {
	case len(req.Changes) == 0:
		return apitypes.DataChangeset{}, invalid("a changeset needs at least one change")
	case len(req.Changes) > apitypes.DataChangesetMaxRows:
		return apitypes.DataChangeset{}, invalid("a changeset has at most %d changes; split it", apitypes.DataChangesetMaxRows)
	case len(req.Message) > 1000:
		return apitypes.DataChangeset{}, invalid("the message is longer than 1000 characters")
	case a.Kind != "user" && a.Kind != "token":
		return apitypes.DataChangeset{}, invalid("changesets need a user or token as author")
	}
	dt, err := c.dataTarget(ctx, a, ref, apitypes.LevelPropose, "proposing changes")
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	if err := checkSensitiveChanges(dt, req.Changes); err != nil {
		return apitypes.DataChangeset{}, err
	}
	sql, err := c.data.Render(ctx, dt.t, req.Changes)
	if err != nil {
		return apitypes.DataChangeset{}, dataError(dt, err)
	}
	if req.DryRun {
		return apitypes.DataChangeset{
			Status: apitypes.ChangesetPreview, Author: a.Name, AuthorKind: a.Kind, Message: req.Message,
			Changes: req.Changes, SQL: sql, ConfirmCommit: dt.perm.ConfirmCommit, CreatedAt: now(),
		}, nil
	}
	changes, err := json.Marshal(req.Changes)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	params := store.CreateChangesetParams{
		ServiceID: dt.ss.svc.ID, Author: a.Name, AuthorKind: a.Kind, Message: req.Message,
		Changes: string(changes), Sql: sql, CreatedAt: now(),
	}
	params.ExpiresAt = params.CreatedAt.Add(apitypes.DataChangesetExpiry)
	if a.UserID != 0 {
		params.UserID = &a.UserID
	}
	if a.TokenID != 0 {
		params.TokenID = &a.TokenID
	}
	cs, err := c.store.CreateChangeset(ctx, params)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	summary := fmt.Sprintf("proposed changeset %d: %s", cs.ID, summarizeChanges(req.Changes))
	if req.Message != "" {
		summary += " (" + req.Message + ")"
	}
	c.audit(ctx, a, apitypes.AuditDataPropose, &dt.ss.project, &dt.ss.env, dt.ss.String(), summary)
	return toChangeset(cs, dt.perm.ConfirmCommit)
}

// loadChangeset expires overdue changesets, then loads one of the
// database's.
func (c *Core) loadChangeset(ctx context.Context, dt *dbTarget, id int64) (store.Changeset, error) {
	if _, err := c.store.ExpireChangesets(ctx, now()); err != nil {
		return store.Changeset{}, err
	}
	cs, err := c.store.GetChangeset(ctx, store.GetChangesetParams{ID: id, ServiceID: dt.ss.svc.ID})
	if store.IsNotFound(err) {
		return cs, notFound("changeset %d not found in %s", id, dt.ss)
	}
	return cs, err
}

// ListDataChangesets returns the database's changesets, newest first (at
// most 200); status "" means all. Needs level read.
func (c *Core) ListDataChangesets(ctx context.Context, a Actor, ref ServiceRef, status apitypes.DataChangesetStatus) ([]apitypes.DataChangeset, error) {
	switch status {
	case "", apitypes.ChangesetPending, apitypes.ChangesetCommitted, apitypes.ChangesetRejected, apitypes.ChangesetExpired, apitypes.ChangesetConflict:
	default:
		return nil, invalid("status must be pending, committed, rejected, expired or conflict")
	}
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	if err := requireLevel(dt.perm, apitypes.LevelRead, "listing changesets"); err != nil {
		return nil, err
	}
	if _, err := c.store.ExpireChangesets(ctx, now()); err != nil {
		return nil, err
	}
	var rows []store.Changeset
	if status == apitypes.ChangesetPending {
		rows, err = c.store.ListPendingChangesets(ctx, dt.ss.svc.ID)
		slices.Reverse(rows)
	} else {
		rows, err = c.store.ListChangesets(ctx, store.ListChangesetsParams{ServiceID: dt.ss.svc.ID, BeforeID: math.MaxInt64, MaxRows: 200})
	}
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataChangeset, 0, len(rows))
	for _, r := range rows {
		if status != "" && r.Status != string(status) {
			continue
		}
		cs, err := toChangeset(r, dt.perm.ConfirmCommit)
		if err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	return out, nil
}

// GetDataChangeset returns one changeset. Needs level read.
func (c *Core) GetDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64) (apitypes.DataChangeset, error) {
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	if err := requireLevel(dt.perm, apitypes.LevelRead, "reading changesets"); err != nil {
		return apitypes.DataChangeset{}, err
	}
	cs, err := c.loadChangeset(ctx, dt, id)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return toChangeset(cs, dt.perm.ConfirmCommit)
}

// decidable loads a pending changeset for a decision and claims it; call
// c.deciding.done(id) when finished.
func (c *Core) decidable(ctx context.Context, dt *dbTarget, id int64, verb string) (store.Changeset, error) {
	cs, err := c.loadChangeset(ctx, dt, id)
	if err != nil {
		return cs, err
	}
	if cs.Status != string(apitypes.ChangesetPending) {
		return cs, conflict("changeset %d is %s; only pending changesets can be %s", id, cs.Status, verb)
	}
	if !c.deciding.claim(id) {
		return cs, conflict("changeset %d is being decided by someone else; refresh in a moment", id)
	}
	return cs, nil
}

// CommitDataChangeset revalidates and commits a pending changeset: one's
// own with level write, anyone's (approving) as admin. Production needs
// req.Confirm to be the environment's name. Rows that no longer match
// what the author saw fail the whole changeset: it ends as conflict, with
// the rows listed, and nothing is written. Audited as data.commit (with
// the SQL as detail) and, for someone else's, data.approve.
func (c *Core) CommitDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.DataCommitRequest) (apitypes.DataChangeset, error) {
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	cs, err := c.loadChangeset(ctx, dt, id)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	own := isAuthor(a, cs)
	if own {
		if err := requireLevel(dt.perm, apitypes.LevelWrite, "committing your own changeset"); err != nil {
			return apitypes.DataChangeset{}, err
		}
	} else if !dt.perm.Approve {
		return apitypes.DataChangeset{}, errf(apitypes.CodeForbidden, "committing someone else's changeset approves it, which only admins can do (%s)", whyWords(dt.perm))
	}
	if dt.perm.ConfirmCommit && req.Confirm != dt.ss.env.Name {
		return apitypes.DataChangeset{}, invalid("%s is production: confirm with the environment's name (%s) to commit", dt.ss.envScope, dt.ss.env.Name)
	}
	if dt.access == nil {
		return apitypes.DataChangeset{}, errNotSetUp(dt)
	}
	if err := c.connect(ctx, dt); err != nil {
		return apitypes.DataChangeset{}, err
	}
	var changes []apitypes.DataChange
	if err := json.Unmarshal([]byte(cs.Changes), &changes); err != nil {
		return apitypes.DataChangeset{}, fmt.Errorf("changeset %d changes: %w", id, err)
	}
	if err := checkSensitiveChanges(dt, changes); err != nil {
		return apitypes.DataChangeset{}, err
	}
	if cs, err = c.decidable(ctx, dt, id, "committed"); err != nil {
		return apitypes.DataChangeset{}, err
	}
	defer c.deciding.done(id)

	decidedAt := now()
	res, err := c.data.Commit(ctx, dt.t, dt.call(a, true), changes)
	if err != nil {
		return apitypes.DataChangeset{}, dataError(dt, err)
	}
	params := store.DecideChangesetParams{ID: id, Status: string(apitypes.ChangesetCommitted), DecidedAt: &decidedAt, DecidedBy: a.Name}
	outcome, rows := apitypes.OutcomeOK, int64(0)
	if len(res.Conflicts) > 0 {
		b, _ := json.Marshal(res.Conflicts)
		params.Status, params.Conflicts, outcome = string(apitypes.ChangesetConflict), string(b), apitypes.OutcomeConflict
	} else {
		b, _ := json.Marshal(res.RowsAffected)
		params.RowsAffected = string(b)
		for _, n := range res.RowsAffected {
			rows += n
		}
	}
	// The database has decided; record it even if the caller went away.
	ctx = context.WithoutCancel(ctx)
	decided, err := c.store.DecideChangeset(ctx, params)
	if err != nil {
		return apitypes.DataChangeset{}, fmt.Errorf("record the decision on changeset %d: %w", id, err)
	}
	summary := fmt.Sprintf("committed changeset %d: %s, %d rows", id, summarizeChanges(changes), rows)
	if outcome == apitypes.OutcomeConflict {
		summary = fmt.Sprintf("changeset %d conflicted with current data; nothing was written", id)
	}
	if !own {
		summary += ", proposed by " + cs.Author
	}
	c.auditDetail(ctx, a, apitypes.AuditDataCommit, dt.ss, summary, store.AddAuditDetailParams{
		Sql: res.SQL, Rows: rows, DurationMs: res.Duration.Milliseconds(), Outcome: string(outcome),
	})
	if !own && outcome == apitypes.OutcomeOK {
		c.audit(ctx, a, apitypes.AuditDataApprove, &dt.ss.project, &dt.ss.env, dt.ss.String(), fmt.Sprintf("approved changeset %d by %s", id, cs.Author))
	}
	return toChangeset(decided, dt.perm.ConfirmCommit)
}

// RejectDataChangeset rejects a pending changeset: admins, or its author
// withdrawing it. Audited as data.reject.
func (c *Core) RejectDataChangeset(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.DataRejectRequest) (apitypes.DataChangeset, error) {
	if len(req.Reason) > 1000 {
		return apitypes.DataChangeset{}, invalid("the reason is longer than 1000 characters")
	}
	dt, err := c.dataService(ctx, a, ref)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	cs, err := c.loadChangeset(ctx, dt, id)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	if !isAuthor(a, cs) && !dt.perm.Approve {
		return apitypes.DataChangeset{}, errf(apitypes.CodeForbidden, "only admins and its author can reject changeset %d", id)
	}
	if _, err := c.decidable(ctx, dt, id, "rejected"); err != nil {
		return apitypes.DataChangeset{}, err
	}
	defer c.deciding.done(id)
	at := now()
	decided, err := c.store.DecideChangeset(ctx, store.DecideChangesetParams{
		ID: id, Status: string(apitypes.ChangesetRejected), DecidedAt: &at, DecidedBy: a.Name, Reason: req.Reason,
	})
	if store.IsNotFound(err) {
		return apitypes.DataChangeset{}, conflict("changeset %d is no longer pending", id)
	}
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	summary := fmt.Sprintf("rejected changeset %d by %s", id, cs.Author)
	if req.Reason != "" {
		summary += ": " + req.Reason
	}
	c.audit(ctx, a, apitypes.AuditDataReject, &dt.ss.project, &dt.ss.env, dt.ss.String(), summary)
	return toChangeset(decided, dt.perm.ConfirmCommit)
}
