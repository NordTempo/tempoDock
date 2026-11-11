//go:build integration

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/redis/redistest"
	"github.com/NordTempo/tempoDock/internal/data/redis/resp"
)

var servers []*redistest.Server

var owner = data.Credentials{User: "default", Password: redistest.Password}

func TestMain(m *testing.M) {
	var err error
	if servers, err = redistest.StartAll(redistest.Images...); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := func() int {
		defer func() {
			for _, s := range servers {
				s.Stop()
			}
		}()
		for i, s := range servers {
			if err := seed(s, target(s, int64(i+1))); err != nil {
				fmt.Fprintf(os.Stderr, "seed %s: %v\n", s.Image, err)
				return 1
			}
		}
		return m.Run()
	}()
	os.Exit(code)
}

// target is s as database id, with Dock's logins.
func target(s *redistest.Server, id int64) data.Target {
	return data.Target{
		ServiceID: id, Dial: s.Dial, Database: "0",
		Reader:  data.Credentials{User: string(data.RoleReader), Password: "reader-pw"},
		Writer:  data.Credentials{User: string(data.RoleWriter), Password: "writer-pw"},
		Monitor: data.Credentials{User: string(data.RoleMonitor), Password: "monitor-pw"},
	}
}

// ownerDo runs commands as the default user.
func ownerDo(s *redistest.Server, cmds ...[]string) ([]resp.Value, error) {
	nc, err := s.Dial(t0())
	if err != nil {
		return nil, err
	}
	c := resp.New(nc)
	defer func() { _ = c.Close() }()
	_ = nc.SetDeadline(time.Now().Add(10 * time.Second))
	return c.Pipeline(append([][]string{{"AUTH", redistest.Password}}, cmds...)...)
}

// seed writes the test keys and sets Dock's users up.
func seed(s *redistest.Server, t data.Target) error {
	cmds := [][]string{
		{"SET", "greeting", "hello"},
		{"SET", "big:string", strings.Repeat("x", 20_000)},
		{"SET", "bin\xff", "\xff\x01"},
		{"SET", "session:1", "token", "PX", "600000"},
		{"HSET", "user:1", "name", "Ada", "email", "ada@x.io"},
		{"SADD", "tags", "a", "b", "c"},
		{"ZADD", "board", "10", "ada", "20.5", "bo"},
		{"XADD", "events", "1-1", "kind", "signup"},
		{"XADD", "events", "2-1", "kind", "order"},
		{"SELECT", "1"},
		{"SET", "other-db", "1"},
		{"SELECT", "0"},
	}
	list := []string{"RPUSH", "queue"}
	for i := range 250 {
		list = append(list, "job-"+strconv.Itoa(i))
	}
	big := []string{"HSET", "big:hash"}
	for i := range 15_000 {
		big = append(big, "f"+strconv.Itoa(i), "v")
	}
	cmds = append(cmds, list, big)
	for i := range 300 {
		cmds = append(cmds, []string{"SET", "cache:" + strconv.Itoa(i), "x"})
	}
	replies, err := ownerDo(s, cmds...)
	if err != nil {
		return err
	}
	for i, r := range replies {
		if err := r.Err(); err != nil {
			return fmt.Errorf("command %d: %w", i, err)
		}
	}
	e := New(Options{})
	defer e.Close()
	_, err = e.Setup(t0(), t, owner, t.Reader, t.Writer)
	return err
}

// each runs fn per server with a fresh engine and the server's target.
func each(t *testing.T, fn func(t *testing.T, e *Engine, s *redistest.Server, tg data.Target)) {
	for i, s := range servers {
		t.Run(s.Image, func(t *testing.T) {
			e := New(Options{})
			t.Cleanup(e.Close)
			fn(t, e, s, target(s, int64(i+1)))
		})
	}
}

func TestSetupAndUsers(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, s *redistest.Server, tg data.Target) {
		ctx := t.Context()
		if info, err := e.Check(ctx, tg, owner); err != nil || info.VersionNum < minVersion {
			t.Fatalf("check: %+v %v", info, err)
		}
		if _, err := e.Check(ctx, tg, data.Credentials{User: "default", Password: "wrong"}); !errors.Is(err, data.ErrLoginFailed) {
			t.Fatalf("wrong owner password: %v", err)
		}
		info, err := e.Status(ctx, tg)
		if err != nil || !(strings.HasPrefix(info.Version, "Redis ") || strings.HasPrefix(info.Version, "Valkey ")) {
			t.Fatalf("status: %+v %v", info, err)
		}

		// The reader can't write, run KEYS or read the config (which holds
		// requirepass); the monitor can't touch keys.
		for _, tc := range []struct {
			user, pw string
			cmd      []string
		}{
			{string(data.RoleReader), "reader-pw", []string{"SET", "x", "1"}},
			{string(data.RoleReader), "reader-pw", []string{"KEYS", "*"}},
			{string(data.RoleReader), "reader-pw", []string{"CONFIG", "GET", "requirepass"}},
			{string(data.RoleReader), "reader-pw", []string{"FLUSHALL"}},
			{string(data.RoleReader), "reader-pw", []string{"EVAL", "return 1", "0"}},
			{string(data.RoleWriter), "writer-pw", []string{"FLUSHDB"}},
			{string(data.RoleWriter), "writer-pw", []string{"RENAME", "greeting", "g2"}},
		} {
			c, err := e.open(ctx, tg, data.Credentials{User: tc.user, Password: tc.pw}, false)
			if err != nil {
				t.Fatal(err)
			}
			v, err := c.Do(tc.cmd...)
			_ = c.Close()
			if err != nil || v.Err() == nil || !strings.HasPrefix(v.Str, "NOPERM") {
				t.Errorf("%s %v: %+v %v", tc.user, tc.cmd, v, err)
			}
		}

		// A restart drops ACL users: logins fail with ErrNoPrivilege until
		// the grants sync puts them back.
		if _, err := ownerDo(s, []string{"ACL", "DELUSER", string(data.RoleReader)}); err != nil {
			t.Fatal(err)
		}
		e.Forget(tg.ServiceID)
		if _, err := e.Status(ctx, tg); !errors.Is(err, data.ErrNoPrivilege) {
			t.Fatalf("after the user went: %v", err)
		}
		if _, err := e.SyncGrants(ctx, tg, owner, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Status(ctx, tg); err != nil {
			t.Fatalf("after the sync: %v", err)
		}
	})
}

func TestKeyspaceAndScan(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, s *redistest.Server, tg data.Target) {
		ctx := t.Context()
		ks, err := e.Keyspace(ctx, tg, data.Call{})
		if err != nil || len(ks.Keyspace) < 2 || ks.Keyspace[0].DB != 0 || ks.Keyspace[0].Keys < 300 || ks.Keyspace[1].Keys != 1 {
			t.Fatalf("keyspace: %+v %v", ks, err)
		}
		// Every key, page by page.
		seen := map[string]apitypes.DataKeyInfo{}
		req := apitypes.DataKeysRequest{Limit: 50}
		for range 100 {
			p, err := e.ScanKeys(ctx, tg, data.Call{}, req)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range p.Keys {
				seen[k.Key] = k
			}
			if p.Next == "" {
				break
			}
			req.After = p.Next
		}
		if len(seen) != int(ks.Keyspace[0].Keys) {
			t.Fatalf("scanned %d keys of %d", len(seen), ks.Keyspace[0].Keys)
		}
		if k := seen["queue"]; k.Type != "list" || k.Length != 250 || k.TTLMs != -1 || k.Bytes == 0 {
			t.Errorf("queue: %+v", k)
		}
		if k := seen["session:1"]; k.TTLMs <= 0 || k.TTLMs > 600_000 {
			t.Errorf("session ttl: %+v", k)
		}
		if k := seen["62696eff"]; !k.Hex || k.Type != "string" {
			t.Errorf("binary key: %+v", seen)
		}
		// MATCH and TYPE go to the server.
		hashes := map[string]bool{}
		req = apitypes.DataKeysRequest{Match: "*", Type: "hash", Limit: 500}
		for range 100 {
			p, err := e.ScanKeys(ctx, tg, data.Call{}, req)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range p.Keys {
				hashes[k.Key] = true
			}
			if p.Next == "" {
				break
			}
			req.After = p.Next
		}
		if len(hashes) != 2 || !hashes["user:1"] || !hashes["big:hash"] {
			t.Errorf("hashes: %v", hashes)
		}
		// Masked callers see sensitive keys by pattern.
		masked := map[string]bool{}
		req = apitypes.DataKeysRequest{Match: "session:*", Limit: 500}
		p, err := e.ScanKeys(ctx, tg, data.Call{Masked: true, SensitiveKeys: []string{"session:*"}}, req)
		for _, k := range p.Keys {
			masked[k.Key] = k.Masked
		}
		if err != nil || len(masked) != 1 || !masked["session:*"] {
			t.Errorf("masked scan: %+v %v", p, err)
		}
		first, _ := e.ScanKeys(ctx, tg, data.Call{}, apitypes.DataKeysRequest{Limit: 10})
		if _, err := e.ScanKeys(ctx, tg, data.Call{}, apitypes.DataKeysRequest{Match: "x*", After: first.Next}); !errors.Is(err, data.ErrInvalid) {
			t.Errorf("foreign cursor: %v", err)
		}
	})
}

// value reads every page of a key and returns its rows' cells.
func value(t *testing.T, e *Engine, tg data.Target, req apitypes.DataKeyRequest) (apitypes.DataKeyValue, [][]any) {
	t.Helper()
	var first apitypes.DataKeyValue
	var cells [][]any
	for i := range 20 {
		v, err := e.Key(t.Context(), tg, data.Call{}, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Key, err)
		}
		if i == 0 {
			first = v
		}
		for _, r := range v.Rows {
			var row []any
			if err := json.Unmarshal(r, &row); err != nil {
				t.Fatal(err)
			}
			cells = append(cells, row)
		}
		if v.Next == "" {
			return first, cells
		}
		req.After = v.Next
	}
	t.Fatal("too many pages")
	return first, nil
}

func TestKeyValues(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, s *redistest.Server, tg data.Target) {
		if v, rows := value(t, e, tg, apitypes.DataKeyRequest{Key: "greeting"}); v.Type != "string" || fmt.Sprint(rows) != "[[hello]]" || v.Length != 5 {
			t.Errorf("string: %+v %v", v, rows)
		}
		big, _ := e.Key(t.Context(), tg, data.Call{}, apitypes.DataKeyRequest{Key: "big:string"})
		if !strings.Contains(string(big.Rows[0]), `"bytes":20000`) {
			t.Errorf("big string not cut: %.80s", big.Rows[0])
		}
		if _, rows := value(t, e, tg, apitypes.DataKeyRequest{Key: "62696eff", Hex: true}); fmt.Sprint(rows) != `[[\xff01]]` {
			t.Errorf("binary: %v", rows)
		}
		if _, rows := value(t, e, tg, apitypes.DataKeyRequest{Key: "queue", Limit: 100}); len(rows) != 250 || rows[249][0] != 249.0 || rows[249][1] != "job-249" {
			t.Errorf("list: %d rows, last %v", len(rows), rows[len(rows)-1])
		}
		if _, rows := value(t, e, tg, apitypes.DataKeyRequest{Key: "big:hash", Limit: 1000}); len(rows) < 15_000 {
			t.Errorf("hash: %d rows", len(rows))
		}
		if _, rows := value(t, e, tg, apitypes.DataKeyRequest{Key: "board"}); fmt.Sprint(rows) != "[[0 ada 10] [1 bo 20.5]]" {
			t.Errorf("zset: %v", rows)
		}
		if _, rows := value(t, e, tg, apitypes.DataKeyRequest{Key: "events", Limit: 1}); fmt.Sprint(rows) != `[[2-1 {"kind":"order"}] [1-1 {"kind":"signup"}]]` {
			t.Errorf("stream: %v", rows)
		}
		if _, rows := value(t, e, tg, apitypes.DataKeyRequest{Key: "other-db", DB: 1}); fmt.Sprint(rows) != "[[1]]" {
			t.Errorf("db 1: %v", rows)
		}
		if _, err := e.Key(t.Context(), tg, data.Call{}, apitypes.DataKeyRequest{Key: "nope"}); !errors.Is(err, data.ErrNoTable) {
			t.Errorf("missing key: %v", err)
		}
	})
}

// run runs a console command and returns its frames.
func run(t *testing.T, e *Engine, tg data.Target, cmd string) ([]apitypes.DataQueryFrame, error) {
	t.Helper()
	var frames []apitypes.DataQueryFrame
	_, err := e.Query(t.Context(), tg, data.Call{}, apitypes.DataQueryRequest{SQL: cmd}, func(f apitypes.DataQueryFrame) error {
		frames = append(frames, f)
		return nil
	})
	return frames, err
}

func TestConsole(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, s *redistest.Server, tg data.Target) {
		f, err := run(t, e, tg, `HGETALL "user:1"`)
		if err != nil || len(f) != 3 || f[0].Columns[0].Name != "field" || len(f[1].Rows) != 2 || f[2].Command != "HGETALL 2" {
			t.Fatalf("hgetall: %+v %v", f, err)
		}
		if f, err := run(t, e, tg, "GET greeting"); err != nil || string(f[1].Rows[0]) != `["hello"]` {
			t.Fatalf("get: %+v %v", f, err)
		}
		if f, err := run(t, e, tg, "ZRANGE board 0 -1 WITHSCORES"); err != nil || f[0].Columns[1].Name != "score" || len(f[1].Rows) != 2 {
			t.Fatalf("zrange: %+v %v", f, err)
		}
		if f, err := run(t, e, tg, "SCAN 0 MATCH cache:* COUNT 10"); err != nil || len(f) < 2 {
			t.Fatalf("scan: %+v %v", f, err)
		}
		if f, err := run(t, e, tg, "LRANGE greeting 0 1"); err != nil || f[len(f)-1].Type != apitypes.FrameError || !strings.Contains(f[len(f)-1].Error.Message, "WRONGTYPE") {
			t.Fatalf("wrong type: %+v %v", f, err)
		}
		for _, cmd := range []string{"HGETALL big:hash", "KEYS *", "SET a b", "FLUSHALL", "SELECT 1", "CONFIG GET requirepass", "GET a\nGET b", `GET "open`} {
			if _, err := run(t, e, tg, cmd); !errors.Is(err, data.ErrInvalid) {
				t.Errorf("%q: %v", cmd, err)
			}
		}
		// A whole-value read under the size bound runs.
		if f, err := run(t, e, tg, "LRANGE queue 0 -1"); err != nil || f[2].RowCount != 250 {
			t.Errorf("small lrange: %+v %v", f, err)
		}
		if got := e.Redact(`HSET user:1 email "ada@x.io"`); got != `HSET "user:1" ? ?` {
			t.Errorf("redact: %s", got)
		}
	})
}

// t0 is a context for setup outside a test.
func t0() context.Context { return context.Background() }

func TestCommitKeys(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, s *redistest.Server, tg data.Target) {
		ctx := t.Context()
		str := func(v string) *string { return &v }
		ms := func(v int64) *int64 { return &v }
		seed := [][]string{
			{"DEL", "ch:s", "ch:h", "ch:set", "ch:z", "ch:l", "ch:new"},
			{"SET", "ch:s", "old", "PX", "600000"},
			{"HSET", "ch:h", "a", "1"},
			{"SADD", "ch:set", "x"},
			{"ZADD", "ch:z", "1", "m"},
			{"RPUSH", "ch:l", "a", "b"},
		}
		if _, err := ownerDo(s, seed...); err != nil {
			t.Fatal(err)
		}
		changes := []apitypes.DataKeyChange{
			{Op: apitypes.KeySet, Key: "ch:s", Value: str("new \"v\""), Old: &apitypes.DataKeyOld{Type: "string", Value: str("old")}},
			{Op: apitypes.KeyHashSet, Key: "ch:h", Field: str("b"), Value: str("2"), Old: &apitypes.DataKeyOld{Type: "hash"}},
			{Op: apitypes.KeyHashDelete, Key: "ch:h", Field: str("a"), Old: &apitypes.DataKeyOld{Type: "hash", Value: str("1")}},
			{Op: apitypes.KeySetAdd, Key: "ch:set", Field: str("y"), Old: &apitypes.DataKeyOld{Type: "set"}},
			{Op: apitypes.KeyZAdd, Key: "ch:z", Field: str("m"), Value: str("5"), Old: &apitypes.DataKeyOld{Type: "zset", Value: str("1.0")}},
			{Op: apitypes.KeyListSet, Key: "ch:l", Field: str("0"), Value: str("A"), Old: &apitypes.DataKeyOld{Type: "list", Value: str("a")}},
			{Op: apitypes.KeyListPush, Key: "ch:l", Value: str("c")},
			{Op: apitypes.KeySet, Key: "ch:new", Value: str("1"), Old: &apitypes.DataKeyOld{Type: "none"}},
			{Op: apitypes.KeyExpire, Key: "ch:new", TTLMs: ms(60_000), Old: &apitypes.DataKeyOld{Type: "none"}},
		}
		script, err := e.RenderKeys(ctx, tg, changes)
		if err != nil || !strings.Contains(script, `SET ch:s "new \"v\"" KEEPTTL`) || !strings.HasPrefix(script, "SELECT 0\nWATCH ch:s ch:h ch:set ch:z ch:l ch:new\nMULTI\n") {
			t.Fatalf("render: %s %v", script, err)
		}
		res, err := e.CommitKeys(ctx, tg, data.Call{}, changes)
		if err != nil || len(res.Conflicts) != 0 || len(res.RowsAffected) != len(changes) {
			t.Fatalf("commit: %+v %v", res, err)
		}
		got, err := ownerDo(s, []string{"GET", "ch:s"}, []string{"PTTL", "ch:s"}, []string{"HGETALL", "ch:h"}, []string{"SCARD", "ch:set"},
			[]string{"ZSCORE", "ch:z", "m"}, []string{"LRANGE", "ch:l", "0", "-1"}, []string{"PTTL", "ch:new"})
		if err != nil {
			t.Fatal(err)
		}
		if got[1].Str != `new "v"` || got[2].Int <= 0 || len(got[3].Arr) != 2 || got[4].Int != 2 || got[5].Str != "5" ||
			fmt.Sprint(plain(got[6])) != "[A b c]" || got[7].Int <= 0 {
			t.Fatalf("after commit: %+v", got)
		}

		// A stale old value, a key that went and one whose type changed:
		// conflicts, and nothing of the changeset is written.
		if _, err := ownerDo(s, []string{"DEL", "ch:set"}); err != nil {
			t.Fatal(err)
		}
		stale := []apitypes.DataKeyChange{
			{Op: apitypes.KeyHashDelete, Key: "ch:h", Field: str("b"), Old: &apitypes.DataKeyOld{Type: "hash"}},
			{Op: apitypes.KeySet, Key: "ch:s", Value: str("x"), Old: &apitypes.DataKeyOld{Type: "string", Value: str("old")}},
			{Op: apitypes.KeySetRemove, Key: "ch:set", Field: str("x"), Old: &apitypes.DataKeyOld{Type: "set", Value: str("1")}},
			{Op: apitypes.KeyHashSet, Key: "ch:s", Field: str("f"), Value: str("v"), Old: &apitypes.DataKeyOld{Type: "hash"}},
		}
		res, err = e.CommitKeys(ctx, tg, data.Call{}, stale)
		reasons := map[int]apitypes.DataConflictReason{}
		for _, c := range res.Conflicts {
			reasons[c.Change] = c.Reason
		}
		if err != nil || reasons[0] != apitypes.ConflictChanged || reasons[1] != apitypes.ConflictChanged || reasons[2] != apitypes.ConflictMissing || reasons[3] != apitypes.ConflictChanged {
			t.Fatalf("conflicts: %+v %v", res, err)
		}
		if v, _ := ownerDo(s, []string{"HEXISTS", "ch:h", "b"}); v[1].Int != 1 {
			t.Error("a conflicting changeset wrote")
		}
		for _, bad := range [][]apitypes.DataKeyChange{
			{{Op: apitypes.KeySet, Key: "ch:s", Value: str("x")}},
			{{Op: apitypes.KeyZAdd, Key: "ch:z", Field: str("m"), Value: str("high"), Old: &apitypes.DataKeyOld{Type: "zset"}}},
			{{Op: apitypes.KeySet, Key: "a", Value: str("1"), Old: &apitypes.DataKeyOld{Type: "none"}}, {Op: apitypes.KeySet, DB: 1, Key: "b", Value: str("1"), Old: &apitypes.DataKeyOld{Type: "none"}}},
			{{Op: "rename", Key: "a"}},
		} {
			if _, err := e.RenderKeys(ctx, tg, bad); !errors.Is(err, data.ErrInvalid) {
				t.Errorf("%+v: %v", bad, err)
			}
		}
	})
}

func TestInsights(t *testing.T) {
	each(t, func(t *testing.T, e *Engine, s *redistest.Server, tg data.Target) {
		ctx := t.Context()
		// An app client in the middle of a MULTI.
		nc, err := s.Dial(ctx)
		if err != nil {
			t.Fatal(err)
		}
		app := resp.New(nc)
		defer func() { _ = app.Close() }()
		if _, err := app.Pipeline([]string{"AUTH", redistest.Password}, []string{"CLIENT", "SETNAME", "shop"}, []string{"MULTI"}); err != nil {
			t.Fatal(err)
		}
		all := apitypes.InsightViewsOf(apitypes.EngineRedis)
		ins, err := e.Insights(ctx, tg, data.Call{}, apitypes.DataInsightsRequest{Views: all, Order: apitypes.OrderCalls, Limit: 50})
		if err != nil || len(ins.Unavailable) != 0 {
			t.Fatalf("insights: %+v %v", ins.Unavailable, err)
		}
		if ov := ins.Overview; ov == nil || ov.Keys < 300 || ov.Bytes == 0 || ov.Connections.Max == 0 || ov.EvictionPolicy == "" {
			t.Fatalf("overview: %+v", ins.Overview)
		}
		if len(ins.Queries) == 0 || ins.Queries[0].Calls < ins.Queries[len(ins.Queries)-1].Calls || !slices.ContainsFunc(ins.Queries, func(q apitypes.DataStatement) bool { return q.Text == "SET" }) {
			t.Fatalf("queries: %+v", ins.Queries)
		}
		i := slices.IndexFunc(ins.Sessions, func(s apitypes.DataSession) bool { return s.Application == "shop" })
		if i < 0 || ins.Sessions[i].State != "idle_in_transaction" || ins.Sessions[i].Kind != "client" ||
			!slices.ContainsFunc(ins.Sessions, func(s apitypes.DataSession) bool { return s.Kind == "dock" }) {
			t.Fatalf("sessions: %+v", ins.Sessions)
		}
		cache := slices.IndexFunc(ins.Keys, func(p apitypes.DataKeyPrefix) bool { return p.DB == 0 && p.Prefix == "cache:" })
		if cache < 0 || ins.Keys[cache].EstimatedKeys < 250 || ins.Keys[cache].Biggest == "" ||
			!slices.ContainsFunc(ins.Keys, func(p apitypes.DataKeyPrefix) bool { return p.DB == 1 }) {
			t.Fatalf("keys: %+v", ins.Keys)
		}

		// Sensitive keys show by pattern to masked callers.
		masked, err := e.Insights(ctx, tg, data.Call{Masked: true, SensitiveKeys: []string{"session:*"}}, apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightKeys}})
		if err != nil || !slices.ContainsFunc(masked.Keys, func(p apitypes.DataKeyPrefix) bool { return p.Biggest == "session:*" }) {
			t.Fatalf("masked keys: %+v %v", masked.Keys, err)
		}

		// Ending the client; cancelling a command is refused.
		id := ins.Sessions[i].ID
		if _, err := e.Cancel(ctx, tg, owner, id, nil, false); !errors.Is(err, data.ErrInvalid) {
			t.Fatalf("cancel: %v", err)
		}
		res, err := e.Cancel(ctx, tg, owner, id, nil, true)
		if err != nil || !res.Found || res.Application != "shop" {
			t.Fatalf("terminate: %+v %v", res, err)
		}
		if res, err := e.Cancel(ctx, tg, owner, id, nil, true); err != nil || res.Found {
			t.Fatalf("terminate again: %+v %v", res, err)
		}

		if err := e.ResetQueryStats(ctx, tg, owner); err != nil {
			t.Fatal(err)
		}
		after, err := e.Insights(ctx, tg, data.Call{}, apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightQueries}})
		if err != nil || slices.ContainsFunc(after.Queries, func(q apitypes.DataStatement) bool { return q.Text == "SET" }) {
			t.Fatalf("after reset: %+v %v", after.Queries, err)
		}

		// A dropped monitor (a restart drops ACL users) is not set up, and
		// EnsureMonitor creates it again.
		if _, err := ownerDo(s, []string{"ACL", "DELUSER", string(data.RoleMonitor)}); err != nil {
			t.Fatal(err)
		}
		e.Forget(tg.ServiceID)
		if _, err := e.Pressure(ctx, tg); !errors.Is(err, data.ErrNotSetUp) {
			t.Fatalf("pressure without monitor: %v", err)
		}
		if err := e.EnsureMonitor(ctx, tg, owner); err != nil {
			t.Fatal(err)
		}
		if p, err := e.Pressure(ctx, tg); err != nil || p.Used == 0 {
			t.Fatalf("pressure: %+v %v", p, err)
		}
	})
}
