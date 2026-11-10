package datapanel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// Changesets (ADR 0016 section 5): row changes previewed, stored as
// pending, then committed in one transaction (by their author with level
// write, or approved by someone with Approve) or rejected. Pending ones
// expire after apitypes.DataChangesetExpiry; nothing runs on a timer, they
// are expired lazily (Host.ExpireChangesets) before they are read.

// decisions keeps two decisions on one changeset (two approvals, an
// approval and a rejection) from running at once in this process: a
// commit must not run twice against the database.
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

// changesetFor converts a stored changeset for tg's caller. Callers who
// see sensitive columns masked (tg.sensitive loaded) get a changeset that
// touches one masked too: those columns' values are left out of its
// changes and conflicts, and the literals in its SQL become ?. The
// host's changeset is never modified.
func (p *Panel[D]) changesetFor(tg *target[D], cs Changeset) apitypes.DataChangeset {
	expires := cs.ExpiresAt
	out := apitypes.DataChangeset{
		ID: cs.ID, Status: cs.Status, Author: cs.Author, AuthorKind: cs.AuthorKind,
		Message: cs.Message, Changes: cs.Changes, KeyChanges: cs.KeyChanges, SQL: cs.SQL, RowsAffected: cs.RowsAffected, Conflicts: cs.Conflicts,
		ConfirmCommit: tg.db.Perm.ConfirmCommit, CreatedAt: cs.CreatedAt, ExpiresAt: &expires,
		DecidedAt: cs.DecidedAt, DecidedBy: cs.DecidedBy, Reason: cs.Reason,
	}
	if tg.db.Perm.Sensitive == apitypes.SensitiveVisible {
		return out
	}
	if changes, conflicts, touched := maskChanges(out.Changes, out.Conflicts, tg.sensitive); touched {
		out.Changes, out.Conflicts, out.SQL = changes, conflicts, p.redact(tg.db.Engine, out.SQL)
	}
	if keys, conflicts, touched := maskKeyChanges(out.KeyChanges, out.Conflicts, tg.sensitiveKeys); touched {
		out.KeyChanges, out.Conflicts, out.SQL = keys, conflicts, redactedText
	}
	return out
}

// maskKeyChanges returns key changes with keys matching a sensitive
// pattern shown as the pattern and without values, conflicts on them
// without current values, and whether there were any.
func maskKeyChanges(changes []apitypes.DataKeyChange, conflicts []apitypes.DataConflict, patterns []string) ([]apitypes.DataKeyChange, []apitypes.DataConflict, bool) {
	touched := false
	for i, ch := range changes {
		pattern, ok := data.KeySensitive(patterns, ch.Key)
		if !ok {
			continue
		}
		if !touched {
			touched = true
			changes, conflicts = slices.Clone(changes), slices.Clone(conflicts)
		}
		changes[i] = apitypes.DataKeyChange{Op: ch.Op, DB: ch.DB, Key: pattern}
		for j, cf := range conflicts {
			if cf.Change == i {
				conflicts[j].Current = nil
			}
		}
	}
	return changes, conflicts, touched
}

// checkSensitiveKeyChanges refuses key changes to keys the caller sees
// masked.
func checkSensitiveKeyChanges[D any](tg *target[D], changes []apitypes.DataKeyChange) error {
	if tg.db.Perm.Sensitive == apitypes.SensitiveVisible {
		return nil
	}
	for _, ch := range changes {
		if pattern, ok := data.KeySensitive(tg.sensitiveKeys, ch.Key); ok {
			return forbidden("keys matching %s are sensitive and masked for you; leave them out of the changes", pattern)
		}
	}
	return nil
}

// summarizeKeyChanges says what a key changeset touches, e.g. "3 changes
// to keys in db 0". Key names are data: they stay out of summaries.
func summarizeKeyChanges(changes []apitypes.DataKeyChange) string {
	noun := "changes"
	if len(changes) == 1 {
		noun = "change"
	}
	db := 0
	if len(changes) > 0 {
		db = changes[0].DB
	}
	return fmt.Sprintf("%d %s to keys in db %d", len(changes), noun, db)
}

// touches reports whether a change reads or writes col.
func touches(ch apitypes.DataChange, col apitypes.DataColumnRef) bool {
	if ch.Table != col.Schema+"."+col.Table {
		return false
	}
	_, inKey := ch.Key[col.Column]
	_, inOld := ch.Old[col.Column]
	_, inNew := ch.New[col.Column]
	return inKey || inOld || inNew
}

// maskChanges returns changes and conflicts without the sensitive
// columns' values, and whether there were any. It copies what it
// changes; the arguments stay as they are.
func maskChanges(changes []apitypes.DataChange, conflicts []apitypes.DataConflict, sensitive []apitypes.DataColumnRef) ([]apitypes.DataChange, []apitypes.DataConflict, bool) {
	touched := false
	for i, ch := range changes {
		for _, col := range sensitive {
			if !touches(ch, col) {
				continue
			}
			if !touched {
				touched = true
				changes, conflicts = slices.Clone(changes), slices.Clone(conflicts)
			}
			ch.Key, ch.Old, ch.New = without(ch.Key, col.Column), without(ch.Old, col.Column), without(ch.New, col.Column)
			changes[i] = ch
			for j, cf := range conflicts {
				if cf.Change == i {
					conflicts[j].Current = without(cf.Current, col.Column)
				}
			}
		}
	}
	return changes, conflicts, touched
}

// without returns m without key, copied when it had it.
func without[V any](m map[string]V, key string) map[string]V {
	if _, ok := m[key]; !ok {
		return m
	}
	m = maps.Clone(m)
	delete(m, key)
	return m
}

// isAuthor reports whether the actor wrote the changeset.
func isAuthor(a Actor, cs Changeset) bool {
	switch {
	case a.UserID != 0:
		return cs.UserID == a.UserID
	case a.TokenID != 0:
		return cs.TokenID == a.TokenID
	}
	return false
}

// checkSensitiveChanges refuses changes that read or write a column the
// caller sees masked.
func checkSensitiveChanges[D any](tg *target[D], changes []apitypes.DataChange) error {
	if tg.db.Perm.Sensitive == apitypes.SensitiveVisible {
		return nil
	}
	for _, ch := range changes {
		for _, col := range tg.sensitive {
			if touches(ch, col) {
				return forbidden("%s.%s is sensitive and masked for you; leave it out of the changes", ch.Table, col.Column)
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

// CreateChangeset previews (dry run) or stores a changeset as pending.
// Needs level propose. The changes are validated against the schema and
// rendered to the SQL a commit would run; nothing is written to the
// database. Stored changesets are audited as data.propose and expire
// after apitypes.DataChangesetExpiry.
func (p *Panel[D]) CreateChangeset(ctx context.Context, a Actor, db *Database[D], req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error) {
	keyed := len(req.KeyChanges) > 0
	switch {
	case keyed && len(req.Changes) > 0:
		return apitypes.DataChangeset{}, invalid("a changeset has row changes or key changes, not both")
	case len(req.Changes) == 0 && !keyed:
		return apitypes.DataChangeset{}, invalid("a changeset needs at least one change")
	case len(req.Changes) > apitypes.DataChangesetMaxRows || len(req.KeyChanges) > apitypes.DataChangesetMaxRows:
		return apitypes.DataChangeset{}, invalid("a changeset has at most %d changes; split it", apitypes.DataChangesetMaxRows)
	case len(req.Message) > 1000:
		return apitypes.DataChangeset{}, invalid("the message is longer than 1000 characters")
	case a.Kind != "user" && a.Kind != "token":
		return apitypes.DataChangeset{}, invalid("changesets need a user or token as author")
	}
	tg, err := p.ready(ctx, db, apitypes.LevelPropose, "proposing changes")
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	var sql, what string
	if keyed {
		k, err := keys(tg, "changing keys")
		if err != nil {
			return apitypes.DataChangeset{}, err
		}
		if err := checkSensitiveKeyChanges(tg, req.KeyChanges); err != nil {
			return apitypes.DataChangeset{}, err
		}
		if sql, err = k.RenderKeys(ctx, tg.t, req.KeyChanges); err != nil {
			return apitypes.DataChangeset{}, p.dataError(db, err)
		}
		what = summarizeKeyChanges(req.KeyChanges)
	} else {
		if err := checkSensitiveChanges(tg, req.Changes); err != nil {
			return apitypes.DataChangeset{}, err
		}
		if sql, err = tg.eng.Render(ctx, tg.t, req.Changes); err != nil {
			return apitypes.DataChangeset{}, p.dataError(db, err)
		}
		what = summarizeChanges(req.Changes)
	}
	if req.DryRun {
		return apitypes.DataChangeset{
			Status: apitypes.ChangesetPreview, Author: a.Name, AuthorKind: a.Kind, Message: req.Message,
			Changes: nonNilChanges(req.Changes), KeyChanges: req.KeyChanges, SQL: sql, ConfirmCommit: db.Perm.ConfirmCommit, CreatedAt: now(),
		}, nil
	}
	at := now()
	cs, err := p.host.CreateChangeset(ctx, Changeset{
		Database: db.ID, Author: a.Name, AuthorKind: a.Kind, UserID: a.UserID, TokenID: a.TokenID,
		Status: apitypes.ChangesetPending, Message: req.Message, Changes: nonNilChanges(req.Changes), KeyChanges: req.KeyChanges, SQL: sql,
		CreatedAt: at, ExpiresAt: at.Add(apitypes.DataChangesetExpiry),
	})
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	summary := fmt.Sprintf("proposed changeset %d: %s", cs.ID, what)
	if req.Message != "" {
		summary += " (" + req.Message + ")"
	}
	p.audit(ctx, a, db, apitypes.AuditDataPropose, summary, nil)
	return p.changesetFor(tg, cs), nil
}

// loadChangeset expires overdue changesets, then loads one of the
// database's.
func (p *Panel[D]) loadChangeset(ctx context.Context, db *Database[D], id int64) (Changeset, error) {
	if err := p.host.ExpireChangesets(ctx, now()); err != nil {
		return Changeset{}, err
	}
	cs, err := p.host.Changeset(ctx, db.ID, id)
	if errors.Is(err, ErrNotFound) {
		return cs, notFound("changeset %d not found in %s", id, db.Name)
	}
	return cs, err
}

// sensitiveTarget is db with its sensitive columns (or key patterns: the
// engine says which), for showing changesets to its caller.
func (p *Panel[D]) sensitiveTarget(ctx context.Context, db *Database[D]) (*target[D], error) {
	tg := &target[D]{db: db, eng: p.engines[db.Engine]}
	return tg, p.loadSensitive(ctx, tg)
}

// Changesets returns the database's changesets, newest first (at most
// 200); status "" means all. Needs level read; masked callers get
// sensitive values masked (changesetFor).
func (p *Panel[D]) Changesets(ctx context.Context, a Actor, db *Database[D], status apitypes.DataChangesetStatus) ([]apitypes.DataChangeset, error) {
	switch status {
	case "", apitypes.ChangesetPending, apitypes.ChangesetCommitted, apitypes.ChangesetRejected, apitypes.ChangesetExpired, apitypes.ChangesetConflict:
	default:
		return nil, invalid("status must be pending, committed, rejected, expired or conflict")
	}
	if err := RequireLevel(db.Perm, apitypes.LevelRead, "listing changesets"); err != nil {
		return nil, err
	}
	tg, err := p.sensitiveTarget(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := p.host.ExpireChangesets(ctx, now()); err != nil {
		return nil, err
	}
	rows, err := p.host.Changesets(ctx, db.ID, status == apitypes.ChangesetPending, 200)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataChangeset, 0, len(rows))
	for _, r := range rows {
		if status != "" && r.Status != status {
			continue
		}
		out = append(out, p.changesetFor(tg, r))
	}
	return out, nil
}

// Changeset returns one changeset. Needs level read; masked callers get
// sensitive values masked (changesetFor).
func (p *Panel[D]) Changeset(ctx context.Context, a Actor, db *Database[D], id int64) (apitypes.DataChangeset, error) {
	if err := RequireLevel(db.Perm, apitypes.LevelRead, "reading changesets"); err != nil {
		return apitypes.DataChangeset{}, err
	}
	tg, err := p.sensitiveTarget(ctx, db)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	cs, err := p.loadChangeset(ctx, db, id)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	return p.changesetFor(tg, cs), nil
}

// decidable loads a pending changeset for a decision and claims it; call
// p.deciding.done(id) when finished.
func (p *Panel[D]) decidable(ctx context.Context, db *Database[D], id int64, verb string) (Changeset, error) {
	cs, err := p.loadChangeset(ctx, db, id)
	if err != nil {
		return cs, err
	}
	if cs.Status != apitypes.ChangesetPending {
		return cs, conflict("changeset %d is %s; only pending changesets can be %s", id, cs.Status, verb)
	}
	if !p.deciding.claim(id) {
		return cs, conflict("changeset %d is being decided by someone else; refresh in a moment", id)
	}
	return cs, nil
}

// Commit revalidates and commits a pending changeset: one's own with
// level write, anyone's (approving) with Approve. Production needs
// req.Confirm to be the database's ConfirmName. Rows that no longer match
// what the author saw fail the whole changeset: it ends as conflict, with
// the rows listed, and nothing is written. Audited as data.commit (with
// the SQL as detail) and, for someone else's, data.approve.
func (p *Panel[D]) Commit(ctx context.Context, a Actor, db *Database[D], id int64, req apitypes.DataCommitRequest) (apitypes.DataChangeset, error) {
	cs, err := p.loadChangeset(ctx, db, id)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	own := isAuthor(a, cs)
	if own {
		if err := RequireLevel(db.Perm, apitypes.LevelWrite, "committing your own changeset"); err != nil {
			return apitypes.DataChangeset{}, err
		}
	} else if !db.Perm.Approve {
		return apitypes.DataChangeset{}, forbidden("committing someone else's changeset approves it, which only admins can do (%s)", Why(db.Perm))
	}
	if db.Perm.ConfirmCommit && req.Confirm != db.ConfirmName {
		return apitypes.DataChangeset{}, invalid("%s is production: confirm with %s (%s) to commit", db.Name, p.words.ConfirmWhat, db.ConfirmName)
	}
	tg, err := p.load(ctx, db)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	if tg.access == nil {
		return apitypes.DataChangeset{}, p.errNotSetUp(db)
	}
	if err := p.connect(ctx, tg); err != nil {
		return apitypes.DataChangeset{}, err
	}
	changes, keyed := cs.Changes, len(cs.KeyChanges) > 0
	var k data.Keys
	if keyed {
		if k, err = keys(tg, "changing keys"); err != nil {
			return apitypes.DataChangeset{}, err
		}
		if err := checkSensitiveKeyChanges(tg, cs.KeyChanges); err != nil {
			return apitypes.DataChangeset{}, err
		}
	} else if err := checkSensitiveChanges(tg, changes); err != nil {
		return apitypes.DataChangeset{}, err
	}
	if cs, err = p.decidable(ctx, db, id, "committed"); err != nil {
		return apitypes.DataChangeset{}, err
	}
	defer p.deciding.done(id)

	if err := p.ensureGrants(ctx, tg, tg.call(a, true)); err != nil {
		return apitypes.DataChangeset{}, err
	}
	d := Decision{Database: db.ID, ID: id, Status: apitypes.ChangesetCommitted, DecidedAt: now(), DecidedBy: a.Name}
	var res data.CommitResult
	if keyed {
		res, err = k.CommitKeys(ctx, tg.t, tg.call(a, true), cs.KeyChanges)
	} else {
		res, err = tg.eng.Commit(ctx, tg.t, tg.call(a, true), changes)
	}
	if err != nil {
		return apitypes.DataChangeset{}, p.dataError(db, err)
	}
	outcome, rows := apitypes.OutcomeOK, int64(0)
	if len(res.Conflicts) > 0 {
		d.Status, d.Conflicts, outcome = apitypes.ChangesetConflict, res.Conflicts, apitypes.OutcomeConflict
	} else {
		d.RowsAffected = res.RowsAffected
		for _, n := range res.RowsAffected {
			rows += n
		}
	}
	// The database has decided; record it even if the caller went away.
	ctx = context.WithoutCancel(ctx)
	decided, err := p.host.DecideChangeset(ctx, d)
	if err != nil {
		return apitypes.DataChangeset{}, fmt.Errorf("record the decision on changeset %d: %w", id, err)
	}
	what := summarizeChanges(changes)
	if keyed {
		what = summarizeKeyChanges(cs.KeyChanges)
	}
	summary := fmt.Sprintf("committed changeset %d: %s, %d rows", id, what, rows)
	if outcome == apitypes.OutcomeConflict {
		summary = fmt.Sprintf("changeset %d conflicted with current data; nothing was written", id)
	}
	if !own {
		summary += ", proposed by " + cs.Author
	}
	p.audit(ctx, a, db, apitypes.AuditDataCommit, summary, &AuditDetail{
		SQL: res.SQL, Rows: rows, Duration: res.Duration, Outcome: outcome,
	})
	if !own && outcome == apitypes.OutcomeOK {
		p.audit(ctx, a, db, apitypes.AuditDataApprove, fmt.Sprintf("approved changeset %d by %s", id, cs.Author), nil)
	}
	return p.changesetFor(tg, decided), nil
}

// Reject rejects a pending changeset: someone with Approve, or its author
// withdrawing it. Audited as data.reject.
func (p *Panel[D]) Reject(ctx context.Context, a Actor, db *Database[D], id int64, req apitypes.DataRejectRequest) (apitypes.DataChangeset, error) {
	if len(req.Reason) > 1000 {
		return apitypes.DataChangeset{}, invalid("the reason is longer than 1000 characters")
	}
	cs, err := p.loadChangeset(ctx, db, id)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	if !isAuthor(a, cs) && !db.Perm.Approve {
		return apitypes.DataChangeset{}, forbidden("only admins and its author can reject changeset %d", id)
	}
	tg, err := p.sensitiveTarget(ctx, db)
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	if _, err := p.decidable(ctx, db, id, "rejected"); err != nil {
		return apitypes.DataChangeset{}, err
	}
	defer p.deciding.done(id)
	decided, err := p.host.DecideChangeset(ctx, Decision{
		Database: db.ID, ID: id, Status: apitypes.ChangesetRejected, DecidedAt: now(), DecidedBy: a.Name, Reason: req.Reason,
	})
	if errors.Is(err, ErrNotFound) {
		return apitypes.DataChangeset{}, conflict("changeset %d is no longer pending", id)
	}
	if err != nil {
		return apitypes.DataChangeset{}, err
	}
	summary := fmt.Sprintf("rejected changeset %d by %s", id, cs.Author)
	if req.Reason != "" {
		summary += ": " + req.Reason
	}
	p.audit(ctx, a, db, apitypes.AuditDataReject, summary, nil)
	return p.changesetFor(tg, decided), nil
}

// nonNilChanges is changes, or none (a key changeset's rows).
func nonNilChanges(changes []apitypes.DataChange) []apitypes.DataChange {
	if changes == nil {
		return []apitypes.DataChange{}
	}
	return changes
}
