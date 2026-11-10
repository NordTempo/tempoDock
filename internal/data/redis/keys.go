package redis

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/redis/resp"
)

// Bounds of a key page: at most scanCalls SCAN steps of scanCount each,
// or scanBudget of wall time, whichever ends first.
const (
	scanCalls  = 10
	scanCount  = 200
	scanBudget = 50 * time.Millisecond
	// cellBytes is how much of a value a page keeps (the cell cap); full
	// reads keep fullBytes.
	cellBytes = apitypes.DataCellMaxBytes
	fullBytes = 1 << 20
	// memorySamples is MEMORY USAGE's SAMPLES: an estimate for big
	// collections at a bounded cost (0 would walk the whole value).
	memorySamples = "5"
)

// keyTypes are the types SCAN TYPE and the browser know.
var keyTypes = map[string]bool{"string": true, "hash": true, "list": true, "set": true, "zset": true, "stream": true}

// lengthCmd is the O(1) command that sizes a key of a type.
var lengthCmd = map[string]string{"string": "STRLEN", "hash": "HLEN", "list": "LLEN", "set": "SCARD", "zset": "ZCARD", "stream": "XLEN"}

// Keyspace reads INFO keyspace as the reader: each logical database with
// keys, and database 0 even when empty.
func (e *Engine) Keyspace(ctx context.Context, t data.Target, _ data.Call) (apitypes.DataSchema, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	defer e.release(cn)
	v, err := call(ctx, cn, func(c *conn) (resp.Value, error) { return c.Do("INFO", "keyspace") })
	if err != nil {
		return apitypes.DataSchema{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if err := v.Err(); err != nil {
		return apitypes.DataSchema{}, accessError(err)
	}
	out := apitypes.DataSchema{Tables: []apitypes.DataTable{}, Keyspace: parseKeyspace(v.Str), ReadAt: time.Now().UTC()}
	if len(out.Keyspace) == 0 || out.Keyspace[0].DB != 0 {
		out.Keyspace = append([]apitypes.DataKeyspaceDB{{DB: 0}}, out.Keyspace...)
	}
	return out, nil
}

// parseKeyspace reads INFO keyspace lines: "db0:keys=12,expires=3,...".
func parseKeyspace(info string) []apitypes.DataKeyspaceDB {
	var out []apitypes.DataKeyspaceDB
	for line := range strings.SplitSeq(info, "\n") {
		name, fields, ok := strings.Cut(strings.TrimSpace(line), ":")
		n, err := strconv.Atoi(strings.TrimPrefix(name, "db"))
		if !ok || !strings.HasPrefix(name, "db") || err != nil {
			continue
		}
		db := apitypes.DataKeyspaceDB{DB: n}
		for f := range strings.SplitSeq(fields, ",") {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "keys":
				db.Keys, _ = strconv.ParseInt(v, 10, 64)
			case "expires":
				db.Expires, _ = strconv.ParseInt(v, 10, 64)
			}
		}
		out = append(out, db)
	}
	return out
}

// accessError maps a reply error of a structured read: NOPERM (a user
// whose rules are out of date) is ErrNoPrivilege, anything else
// ErrInvalid with the server's words.
func accessError(err error) error {
	if se, ok := errors.AsType[*resp.ServerError](err); ok && se.Code() == "NOPERM" {
		return fmt.Errorf("%w: %s", data.ErrNoPrivilege, se.Msg)
	}
	return fmt.Errorf("%w: %w", data.ErrInvalid, err)
}

// keyCursor is a decoded Next: a check of the request it belongs to and
// the server's position (a SCAN cursor, a list index, a stream ID). The
// panel seals cursors before they leave the daemon.
type keyCursor struct {
	C string `json:"c"`
	P string `json:"p"`
}

var errBadCursor = fmt.Errorf("%w: the cursor doesn't belong to this database, key or filter; start from the first page", data.ErrInvalid)

// check fingerprints what a cursor is valid for.
func check(parts ...string) string {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func encodeCursor(c keyCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s, want string) (string, error) {
	var c keyCursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.C != want {
		return "", errBadCursor
	}
	return c.P, nil
}

// keyName is a key as the API shows it: hex with hex set when it isn't
// UTF-8.
func keyName(k string) (string, bool) {
	if utf8.ValidString(k) {
		return k, false
	}
	return hex.EncodeToString([]byte(k)), true
}

// rawKey is the key a request names.
func rawKey(key string, isHex bool) (string, error) {
	if !isHex {
		return key, nil
	}
	b, err := hex.DecodeString(key)
	if err != nil {
		return "", fmt.Errorf("%w: hex is set but the key isn't hex", data.ErrInvalid)
	}
	return string(b), nil
}

// ScanKeys reads about req.Limit keys: SCAN steps (MATCH and TYPE given
// to the server) until the limit, scanCalls steps or scanBudget, then one
// pipeline for the keys' types (when not filtered by type) and one for
// their TTLs, lengths and sampled memory, with the database's size. With
// c.Masked, keys matching a sensitive pattern come as the pattern.
func (e *Engine) ScanKeys(ctx context.Context, t data.Target, c data.Call, req apitypes.DataKeysRequest) (apitypes.DataKeyPage, error) {
	limit := req.Limit
	if limit == 0 {
		limit = apitypes.DataKeysLimit
	}
	if limit < 0 || limit > apitypes.DataKeysMaxLimit {
		return apitypes.DataKeyPage{}, fmt.Errorf("%w: limit must be 1 to %d", data.ErrInvalid, apitypes.DataKeysMaxLimit)
	}
	if req.Type != "" && !keyTypes[req.Type] {
		return apitypes.DataKeyPage{}, fmt.Errorf("%w: type must be string, hash, list, set, zset or stream", data.ErrInvalid)
	}
	want := check(strconv.Itoa(req.DB), req.Match, req.Type)
	cursor := "0"
	if req.After != "" {
		var err error
		if cursor, err = decodeCursor(req.After, want); err != nil {
			return apitypes.DataKeyPage{}, err
		}
	}
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return apitypes.DataKeyPage{}, err
	}
	defer e.release(cn)
	return call(ctx, cn, func(rc *conn) (apitypes.DataKeyPage, error) {
		if err := rc.selectDB(req.DB); err != nil {
			return apitypes.DataKeyPage{}, err
		}
		var keys []string
		start := time.Now()
		count := strconv.Itoa(min(scanCount, max(limit, 10)))
		for i := 0; i < scanCalls && len(keys) < limit && time.Since(start) < scanBudget; i++ {
			args := []string{"SCAN", cursor}
			if req.Match != "" {
				args = append(args, "MATCH", req.Match)
			}
			args = append(args, "COUNT", count)
			if req.Type != "" {
				args = append(args, "TYPE", req.Type)
			}
			v, err := rc.Do(args...)
			if err != nil {
				return apitypes.DataKeyPage{}, err
			}
			if err := v.Err(); err != nil {
				return apitypes.DataKeyPage{}, accessError(err)
			}
			if len(v.Arr) != 2 {
				return apitypes.DataKeyPage{}, fmt.Errorf("%w: unexpected SCAN reply", data.ErrUnreachable)
			}
			cursor = v.Arr[0].Str
			for _, k := range v.Arr[1].Arr {
				keys = append(keys, k.Str)
			}
			if cursor == "0" {
				break
			}
		}
		page := apitypes.DataKeyPage{Keys: make([]apitypes.DataKeyInfo, len(keys))}
		types := make([]string, len(keys))
		if req.Type != "" {
			for i := range types {
				types[i] = req.Type
			}
		} else if len(keys) > 0 {
			cmds := make([][]string, len(keys))
			for i, k := range keys {
				cmds[i] = []string{"TYPE", k}
			}
			replies, err := rc.Pipeline(cmds...)
			if err != nil {
				return page, err
			}
			for i, r := range replies {
				types[i] = r.Str
			}
		}
		cmds := [][]string{{"DBSIZE"}}
		for i, k := range keys {
			cmds = append(cmds, []string{"PTTL", k}, []string{"MEMORY", "USAGE", k, "SAMPLES", memorySamples})
			if l, ok := lengthCmd[types[i]]; ok {
				cmds = append(cmds, []string{l, k})
			}
		}
		replies, err := rc.Pipeline(cmds...)
		if err != nil {
			return page, err
		}
		page.Total = replies[0].Int
		at := 1
		for i, k := range keys {
			info := apitypes.DataKeyInfo{Type: types[i], TTLMs: replies[at].Int, Bytes: replies[at+1].Int}
			at += 2
			if _, ok := lengthCmd[types[i]]; ok {
				info.Length = replies[at].Int
				at++
			}
			if info.TTLMs < 0 {
				info.TTLMs = -1 // no expiry, or the key vanished since
			}
			info.Key, info.Hex = keyName(k)
			if pattern, ok := data.KeySensitive(c.SensitiveKeys, k); ok && c.Masked {
				info.Key, info.Hex, info.Masked = pattern, false, true
			}
			page.Keys[i] = info
		}
		if cursor != "0" {
			page.Next = encodeCursor(keyCursor{C: want, P: cursor})
		}
		return page, nil
	})
}

// Key reads one key's type, TTL, size and encoding, then one page of its
// value: a range of a string, HSCAN or SSCAN steps, a range of a list or
// sorted set by index, or stream entries newest first. Values are cut at
// the cell cap (1 MB with Full), so a big value costs Dock at most that.
func (e *Engine) Key(ctx context.Context, t data.Target, c data.Call, req apitypes.DataKeyRequest) (apitypes.DataKeyValue, error) {
	limit := req.Limit
	if limit == 0 {
		limit = apitypes.DataKeyValueLimit
	}
	if limit < 0 || limit > apitypes.DataKeyValueMaxLimit {
		return apitypes.DataKeyValue{}, fmt.Errorf("%w: limit must be 1 to %d", data.ErrInvalid, apitypes.DataKeyValueMaxLimit)
	}
	key, err := rawKey(req.Key, req.Hex)
	if err != nil {
		return apitypes.DataKeyValue{}, err
	}
	want := check(strconv.Itoa(req.DB), key)
	pos := ""
	if req.After != "" {
		if pos, err = decodeCursor(req.After, want); err != nil {
			return apitypes.DataKeyValue{}, err
		}
	}
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return apitypes.DataKeyValue{}, err
	}
	defer e.release(cn)
	return call(ctx, cn, func(rc *conn) (apitypes.DataKeyValue, error) {
		if err := rc.selectDB(req.DB); err != nil {
			return apitypes.DataKeyValue{}, err
		}
		tv, err := rc.Do("TYPE", key)
		if err != nil {
			return apitypes.DataKeyValue{}, err
		}
		if err := tv.Err(); err != nil {
			return apitypes.DataKeyValue{}, accessError(err)
		}
		if tv.Str == "none" {
			return apitypes.DataKeyValue{}, fmt.Errorf("%w: no key %q in db %d", data.ErrNoTable, req.Key, req.DB)
		}
		out := apitypes.DataKeyValue{DataKeyInfo: apitypes.DataKeyInfo{Type: tv.Str}, Rows: []apitypes.DataRow{}}
		out.Key, out.Hex = keyName(key)
		cmds := [][]string{{"PTTL", key}, {"MEMORY", "USAGE", key, "SAMPLES", memorySamples}, {"OBJECT", "ENCODING", key}}
		if l, ok := lengthCmd[tv.Str]; ok {
			cmds = append(cmds, []string{l, key})
		}
		replies, err := rc.Pipeline(cmds...)
		if err != nil {
			return out, err
		}
		// PTTL is -1 without an expiry, -2 when the key just went.
		out.TTLMs, out.Bytes, out.Encoding = max(replies[0].Int, -1), replies[1].Int, replies[2].Str
		if len(replies) > 3 {
			out.Length = replies[3].Int
		}
		keep := cellBytes
		if req.Full {
			keep = fullBytes
		}
		rc.MaxBulk = keep + 1
		defer func() { rc.MaxBulk = 0 }()
		next, err := readValue(rc, key, &out, pos, limit, keep)
		if err != nil {
			return out, err
		}
		if next != "" {
			out.Next = encodeCursor(keyCursor{C: want, P: next})
		}
		return out, nil
	})
}

// col is a result column named n of type t.
func col(n, t string) apitypes.DataResultColumn {
	return apitypes.DataResultColumn{Name: n, TypeName: t}
}

// readValue reads one page of key's value into out and returns the
// position of the next page ("" when it was the last).
func readValue(rc *conn, key string, out *apitypes.DataKeyValue, pos string, limit, keep int) (string, error) {
	n := strconv.Itoa(limit)
	row := func(cells ...[]byte) {
		b := []byte{'['}
		for i, c := range cells {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, c...)
		}
		out.Rows = append(out.Rows, append(b, ']'))
	}
	do := func(args ...string) (resp.Value, error) {
		v, err := rc.Do(args...)
		if err == nil {
			err = v.Err()
			if err != nil {
				err = accessError(err)
			}
		}
		return v, err
	}
	index := func() int64 {
		i, _ := strconv.ParseInt(pos, 10, 64)
		return max(i, 0)
	}
	switch out.Type {
	case "string":
		out.Columns = []apitypes.DataResultColumn{col("value", "string")}
		v, err := do("GETRANGE", key, "0", strconv.Itoa(keep-1))
		if err != nil {
			return "", err
		}
		row(cell(v.Str, out.Length, keep))
		return "", nil
	case "hash", "set":
		cmd, cols := "HSCAN", []apitypes.DataResultColumn{col("field", "string"), col("value", "string")}
		if out.Type == "set" {
			cmd, cols = "SSCAN", []apitypes.DataResultColumn{col("member", "string")}
		}
		out.Columns = cols
		v, err := do(cmd, key, cmp.Or(pos, "0"), "COUNT", n)
		if err != nil {
			return "", err
		}
		if len(v.Arr) != 2 {
			return "", fmt.Errorf("%w: unexpected %s reply", data.ErrUnreachable, cmd)
		}
		items := v.Arr[1].Arr
		for i := 0; i < len(items); i += len(cols) {
			cells := make([][]byte, 0, len(cols))
			for j := range cols {
				if i+j < len(items) {
					cells = append(cells, bulkCell(items[i+j], keep))
				}
			}
			row(cells...)
		}
		if v.Arr[0].Str == "0" {
			return "", nil
		}
		return v.Arr[0].Str, nil
	case "list", "zset":
		start := index()
		stop := strconv.FormatInt(start+int64(limit)-1, 10)
		var v resp.Value
		var err error
		if out.Type == "list" {
			out.Columns = []apitypes.DataResultColumn{col("#", "int"), col("value", "string")}
			v, err = do("LRANGE", key, strconv.FormatInt(start, 10), stop)
		} else {
			out.Columns = []apitypes.DataResultColumn{col("#", "int"), col("member", "string"), col("score", "double")}
			v, err = do("ZRANGE", key, strconv.FormatInt(start, 10), stop, "WITHSCORES")
		}
		if err != nil {
			return "", err
		}
		step := 1
		if out.Type == "zset" {
			step = 2
		}
		got := int64(0)
		for i := 0; i+step-1 < len(v.Arr); i += step {
			idx := strconv.AppendInt(nil, start+got, 10)
			if step == 1 {
				row(idx, bulkCell(v.Arr[i], keep))
			} else {
				row(idx, bulkCell(v.Arr[i], keep), data.AppendCell(nil, []byte(v.Arr[i+1].Str), data.CellNumber, false))
			}
			got++
		}
		if start+got >= out.Length || got == 0 {
			return "", nil
		}
		return strconv.FormatInt(start+got, 10), nil
	case "stream":
		out.Columns = []apitypes.DataResultColumn{col("id", "string"), col("fields", "json")}
		end := "+"
		if pos != "" {
			end = "(" + pos
		}
		v, err := do("XREVRANGE", key, end, "-", "COUNT", n)
		if err != nil {
			return "", err
		}
		last := ""
		for _, entry := range v.Arr {
			if len(entry.Arr) != 2 {
				continue
			}
			last = entry.Arr[0].Str
			fields := map[string]string{}
			kv := entry.Arr[1].Arr
			for i := 0; i+1 < len(kv); i += 2 {
				fields[kv[i].Str] = kv[i+1].Str
			}
			text, _ := json.Marshal(fields)
			row(data.AppendJSONString(nil, []byte(last)), cell(string(text), int64(len(text)), keep))
		}
		if len(v.Arr) < limit {
			return "", nil
		}
		return last, nil
	}
	// A module type (JSON, time series, ...): its type and size only.
	out.Columns = []apitypes.DataResultColumn{}
	return "", nil
}

// bulkCell is a reply's bulk string as a cell (see cell).
func bulkCell(v resp.Value, keep int) []byte { return cell(v.Str, v.Len, keep) }

// cell is a value as a JSON cell: \x hex when it isn't UTF-8, and
// {"truncated": ..., "bytes": size} when it is longer than keep (size is
// the full length).
func cell(s string, size int64, keep int) []byte {
	if !utf8.ValidString(s) {
		s = `\x` + hex.EncodeToString([]byte(s))
	}
	if len(s) > keep {
		cut := keep
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
		size = max(size, int64(keep)+1)
	}
	if size <= int64(len(s)) {
		return data.AppendJSONString(nil, []byte(s))
	}
	b := append([]byte(`{"truncated":`), data.AppendJSONString(nil, []byte(s))...)
	b = append(b, `,"bytes":`...)
	b = strconv.AppendInt(b, size, 10)
	return append(b, '}')
}
