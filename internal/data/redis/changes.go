package redis

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/redis/resp"
)

// Key changesets (ADR 0072): every key is WATCHed, what the editor saw
// is compared with what is there now, and the writes run in one
// MULTI/EXEC, which runs nothing when a watched key changed in between.
// Redis doesn't roll back a command that fails inside EXEC, so each
// change's type is checked under WATCH first: the only failures EXEC can
// still meet are ones that abort it whole (OOM under noeviction).

// maxValueBytes caps one value a key change writes.
const maxValueBytes = 512 << 10

// commitFailed is DataConflict.Change when EXEC didn't run (a watched key
// changed after the comparison).
const commitFailed = -1

// keyPlan is one validated key change: the command it runs, the type
// the key must have, and how to read what the editor saw.
type keyPlan struct {
	ch   apitypes.DataKeyChange
	cmd  []string
	want []string // types the key may have now ("none": it may not exist)
	read []string // reads the compared value; nil compares the type only
}

// planKeys validates changes: one database, known ops with what each
// needs, Old on everything but pushes.
func planKeys(changes []apitypes.DataKeyChange) (int, []keyPlan, error) {
	if len(changes) == 0 || len(changes) > apitypes.DataChangesetMaxRows {
		return 0, nil, fmt.Errorf("%w: a changeset has 1 to %d changes", data.ErrInvalid, apitypes.DataChangesetMaxRows)
	}
	db := changes[0].DB
	plans := make([]keyPlan, len(changes))
	for i, ch := range changes {
		p, err := planKey(ch)
		if err == nil && ch.DB != db {
			err = fmt.Errorf("%w: every change of a changeset is in one database (db %d)", data.ErrInvalid, db)
		}
		if err != nil {
			return 0, nil, fmt.Errorf("change %d: %w", i+1, err)
		}
		plans[i] = p
	}
	return db, plans, nil
}

func planKey(ch apitypes.DataKeyChange) (keyPlan, error) {
	p := keyPlan{ch: ch}
	bad := func(format string, args ...any) (keyPlan, error) {
		return p, fmt.Errorf("%w: "+format, append([]any{data.ErrInvalid}, args...)...)
	}
	if ch.Key == "" {
		return bad("name the key")
	}
	field, value := deref(ch.Field), deref(ch.Value)
	if len(value) > maxValueBytes {
		return bad("a value is at most %d KB", maxValueBytes>>10)
	}
	needField := func() bool { return ch.Field != nil && *ch.Field != "" }
	k := ch.Key
	switch ch.Op {
	case apitypes.KeySet:
		if ch.Value == nil {
			return bad("set needs a value")
		}
		p.cmd, p.want, p.read = []string{"SET", k, value, "KEEPTTL"}, []string{"string", "none"}, []string{"GET", k}
	case apitypes.KeyDelete:
		p.cmd, p.want = []string{"UNLINK", k}, nil
	case apitypes.KeyExpire:
		if ch.TTLMs == nil || *ch.TTLMs == 0 || *ch.TTLMs < -1 {
			return bad("expire needs ttl_ms, a positive number of milliseconds or -1 to remove the TTL")
		}
		p.cmd = []string{"PEXPIRE", k, strconv.FormatInt(*ch.TTLMs, 10)}
		if *ch.TTLMs == -1 {
			p.cmd = []string{"PERSIST", k}
		}
	case apitypes.KeyHashSet, apitypes.KeyHashDelete:
		if !needField() {
			return bad("%s needs the field", ch.Op)
		}
		p.want, p.read = []string{"hash", "none"}, []string{"HGET", k, field}
		if ch.Op == apitypes.KeyHashSet {
			if ch.Value == nil {
				return bad("hset needs a value")
			}
			p.cmd = []string{"HSET", k, field, value}
		} else {
			p.cmd, p.want = []string{"HDEL", k, field}, []string{"hash"}
		}
	case apitypes.KeySetAdd, apitypes.KeySetRemove:
		if !needField() {
			return bad("%s needs the member (field)", ch.Op)
		}
		p.read = []string{"SISMEMBER", k, field}
		if ch.Op == apitypes.KeySetAdd {
			p.cmd, p.want = []string{"SADD", k, field}, []string{"set", "none"}
		} else {
			p.cmd, p.want = []string{"SREM", k, field}, []string{"set"}
		}
	case apitypes.KeyZAdd, apitypes.KeyZRemove:
		if !needField() {
			return bad("%s needs the member (field)", ch.Op)
		}
		p.read = []string{"ZSCORE", k, field}
		if ch.Op == apitypes.KeyZAdd {
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return bad("zadd needs a number as value (the score)")
			}
			p.cmd, p.want = []string{"ZADD", k, value, field}, []string{"zset", "none"}
		} else {
			p.cmd, p.want = []string{"ZREM", k, field}, []string{"zset"}
		}
	case apitypes.KeyListSet:
		if _, err := strconv.ParseInt(field, 10, 64); err != nil {
			return bad("lset needs the element's index as field")
		}
		if ch.Value == nil {
			return bad("lset needs a value")
		}
		p.cmd, p.want, p.read = []string{"LSET", k, field, value}, []string{"list"}, []string{"LINDEX", k, field}
	case apitypes.KeyListPush, apitypes.KeyListPushLeft:
		if ch.Value == nil {
			return bad("%s needs a value", ch.Op)
		}
		p.cmd, p.want = []string{strings.ToUpper(string(ch.Op)), k, value}, []string{"list", "none"}
	case apitypes.KeyListRemove:
		if ch.Field == nil {
			return bad("lrem needs the element to remove (field)")
		}
		p.cmd, p.want = []string{"LREM", k, "1", field}, []string{"list"}
	default:
		return bad("unknown key change %q", ch.Op)
	}
	push := ch.Op == apitypes.KeyListPush || ch.Op == apitypes.KeyListPushLeft
	if ch.Old == nil && !push {
		return bad("old missing: send the key's type and the value you saw")
	}
	return p, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// renderScript is the commands as they run, quoted for redis-cli.
func renderScript(db int, plans []keyPlan) string {
	var b strings.Builder
	line := func(args ...string) {
		for i, a := range args {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(quoteArg(a))
		}
		b.WriteByte('\n')
	}
	line("SELECT", strconv.Itoa(db))
	var keys []string
	for _, p := range plans {
		if !slices.Contains(keys, p.ch.Key) {
			keys = append(keys, p.ch.Key)
		}
	}
	line(append([]string{"WATCH"}, keys...)...)
	line("MULTI")
	for _, p := range plans {
		line(p.cmd...)
	}
	line("EXEC")
	return b.String()
}

// quoteArg quotes an argument the way parseCommand reads it back.
func quoteArg(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\r\n\"'\\") && strings.ToValidUTF8(a, "") == a {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(a); i++ {
		switch c := a[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// RenderKeys validates changes and renders the commands a commit runs.
func (e *Engine) RenderKeys(_ context.Context, _ data.Target, changes []apitypes.DataKeyChange) (string, error) {
	db, plans, err := planKeys(changes)
	if err != nil {
		return "", err
	}
	return renderScript(db, plans), nil
}

// CommitKeys applies changes as the writer (see the package doc above):
// WATCH every key, read each key's type and compared value in one
// pipeline, compare with Old, then MULTI ... EXEC. A mismatch unwatches
// and reports conflicts; EXEC answering nil (a watched key changed after
// the comparison) is one conflict for the whole changeset. Either way
// nothing is written.
func (e *Engine) CommitKeys(ctx context.Context, t data.Target, _ data.Call, changes []apitypes.DataKeyChange) (data.CommitResult, error) {
	start := time.Now()
	db, plans, err := planKeys(changes)
	if err != nil {
		return data.CommitResult{}, err
	}
	res := data.CommitResult{SQL: renderScript(db, plans)}
	ctx, cancel := context.WithTimeout(ctx, data.WriteStatementTimeout)
	defer cancel()
	cn, err := e.acquire(ctx, t, data.RoleWriter)
	if err != nil {
		return res, err
	}
	defer e.release(cn)
	return call(ctx, cn, func(rc *conn) (data.CommitResult, error) {
		if err := rc.selectDB(db); err != nil {
			return res, err
		}
		var keys []string
		for _, p := range plans {
			if !slices.Contains(keys, p.ch.Key) {
				keys = append(keys, p.ch.Key)
			}
		}
		cmds := [][]string{append([]string{"WATCH"}, keys...)}
		for _, p := range plans {
			cmds = append(cmds, []string{"TYPE", p.ch.Key})
			if p.read != nil {
				cmds = append(cmds, p.read)
			}
		}
		replies, err := rc.Pipeline(cmds...)
		if err != nil {
			return res, err
		}
		if err := replies[0].Err(); err != nil {
			return res, accessError(err)
		}
		at := 1
		for i, p := range plans {
			typ := replies[at].Str
			at++
			var cur resp.Value
			if p.read != nil {
				cur = replies[at]
				at++
			}
			if c, ok := conflictOf(i, p, typ, cur); ok {
				res.Conflicts = append(res.Conflicts, c)
			}
		}
		if len(res.Conflicts) > 0 {
			if _, err := rc.Do("UNWATCH"); err != nil {
				return res, err
			}
			res.Duration = time.Since(start)
			return res, nil
		}
		tx := [][]string{{"MULTI"}}
		for _, p := range plans {
			tx = append(tx, p.cmd)
		}
		tx = append(tx, []string{"EXEC"})
		replies, err = rc.Pipeline(tx...)
		if err != nil {
			return res, err
		}
		exec := replies[len(replies)-1]
		res.Duration = time.Since(start)
		switch exec.Kind {
		case resp.Error:
			// EXECABORT: a command was refused when queued (OOM, ...).
			for _, r := range replies[1 : len(replies)-1] {
				if r.Kind == resp.Error {
					exec = r
					break
				}
			}
			res.Conflicts = []apitypes.DataConflict{{Change: commitFailed, Reason: apitypes.ConflictFailed, Message: exec.Str}}
			return res, nil
		case resp.Null:
			res.Conflicts = []apitypes.DataConflict{{Change: commitFailed, Reason: apitypes.ConflictChanged, Message: "a key changed while the changeset was committed; nothing was written"}}
			return res, nil
		}
		for i, r := range exec.Arr {
			if r.Kind == resp.Error {
				// A type checked under WATCH can't fail here; if it does, say so.
				return res, fmt.Errorf("change %d failed inside EXEC after the others ran: %s", i+1, r.Str)
			}
			n := int64(1)
			if r.Kind == resp.Int {
				n = r.Int
			}
			res.RowsAffected = append(res.RowsAffected, n)
		}
		return res, nil
	})
}

// conflictOf compares change i's key with what its editor saw: the type
// first, then the compared value (a set member as "1" when present).
func conflictOf(i int, p keyPlan, typ string, cur resp.Value) (apitypes.DataConflict, bool) {
	var now *string
	switch {
	case p.read == nil:
	case p.read[0] == "SISMEMBER":
		if cur.Int == 1 {
			one := "1"
			now = &one
		}
	case cur.Kind == resp.Bulk:
		v := cur.Str
		now = &v
	}
	current := map[string]*string{"type": &typ}
	if p.read != nil {
		current["value"] = now
	}
	missing := func(msg string) (apitypes.DataConflict, bool) {
		return apitypes.DataConflict{Change: i, Reason: apitypes.ConflictMissing, Message: msg}, true
	}
	changed := func(msg string) (apitypes.DataConflict, bool) {
		return apitypes.DataConflict{Change: i, Reason: apitypes.ConflictChanged, Current: current, Message: msg}, true
	}
	if p.want != nil && !slices.Contains(p.want, typ) {
		if typ == "none" {
			return missing("the key no longer exists")
		}
		return changed(fmt.Sprintf("the key is a %s now", typ))
	}
	old := p.ch.Old
	switch {
	case old == nil:
		return apitypes.DataConflict{}, false // a push: nothing to compare
	case old.Type != typ && typ == "none":
		return missing("the key no longer exists")
	case old.Type != typ:
		return changed(fmt.Sprintf("the key was a %s and is a %s now", old.Type, typ))
	case p.read == nil, old.Value == nil && now == nil:
		return apitypes.DataConflict{}, false
	case cur.Cut():
		return changed("the value is too big to compare; read it again in the key list")
	case old.Value != nil && now != nil && (*old.Value == *now || p.read[0] == "ZSCORE" && sameScore(*old.Value, *now)):
		return apitypes.DataConflict{}, false
	}
	return changed("the value changed since it was read")
}

// sameScore compares sorted set scores as numbers ("1" and "1.0").
func sameScore(a, b string) bool {
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	return err1 == nil && err2 == nil && x == y
}
