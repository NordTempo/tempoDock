package redis

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/redis/resp"
)

// wholeMax is the most elements a command that reads a whole value may
// return; bigger values get a pointer to the scanning commands instead.
const wholeMax = 10_000

// stringMax is the longest string GET and HGET may read; past it, use
// GETRANGE.
const stringMax = 1 << 20

// consoleCommands are what the console runs: the reader's read commands
// (aclReader), without the ones Dock itself uses to set connections up.
var consoleCommands = func() map[string]bool {
	m := map[string]bool{"ping": true}
	for _, c := range aclReader {
		m[c] = true
	}
	return m
}()

// containers are commands whose first argument is a subcommand in ACL
// rules ("memory|usage").
var containers = map[string]bool{"memory": true, "object": true, "xinfo": true, "client": true}

// parseCommand splits a command line the way redis-cli does: words
// separated by spaces, "double quoted" strings with \n \r \t \" \\ and
// \xHH escapes, 'single quoted' strings with \' only. One command: a
// newline outside quotes is refused.
func parseCommand(line string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '\n', '\r':
			if strings.TrimSpace(line[i:]) != "" {
				return nil, fmt.Errorf("%w: run one command at a time", data.ErrInvalid)
			}
			i = len(line)
		case ' ', '\t':
			if inWord {
				args, inWord = append(args, cur.String()), false
				cur.Reset()
			}
		case '"', '\'':
			end := -1
			for j := i + 1; j < len(line); j++ {
				d := line[j]
				if d == '\\' && j+1 < len(line) {
					n := line[j+1]
					switch {
					case c == '\'' && n == '\'':
						cur.WriteByte('\'')
					case c == '\'':
						cur.WriteByte('\\')
						cur.WriteByte(n)
					case n == 'n':
						cur.WriteByte('\n')
					case n == 'r':
						cur.WriteByte('\r')
					case n == 't':
						cur.WriteByte('\t')
					case n == 'x' && j+3 < len(line):
						b, err := hex.DecodeString(line[j+2 : j+4])
						if err != nil {
							return nil, fmt.Errorf("%w: bad \\x escape", data.ErrInvalid)
						}
						cur.Write(b)
						j += 2
					default:
						cur.WriteByte(n)
					}
					j++
					continue
				}
				if d == c {
					end = j
					break
				}
				cur.WriteByte(d)
			}
			if end < 0 {
				return nil, fmt.Errorf("%w: unterminated quote", data.ErrInvalid)
			}
			i, inWord = end, true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("%w: the command is empty", data.ErrInvalid)
	}
	return args, nil
}

// commandName is a command's ACL name: "hgetall", "memory|usage".
func commandName(args []string) string {
	name := strings.ToLower(args[0])
	if containers[name] && len(args) > 1 {
		name += "|" + strings.ToLower(args[1])
	}
	return name
}

// redact keeps a command's name (and subcommand) and its first key, and
// replaces every other argument with ?.
func redact(line string) string {
	args, err := parseCommand(line)
	if err != nil {
		return "?"
	}
	out := []string{strings.ToUpper(args[0])}
	rest := args[1:]
	if containers[strings.ToLower(args[0])] && len(rest) > 0 {
		out, rest = append(out, strings.ToUpper(rest[0])), rest[1:]
	}
	for i, a := range rest {
		if i == 0 {
			out = append(out, strconv.Quote(a))
		} else {
			out = append(out, "?")
		}
	}
	return strings.Join(out, " ")
}

// Redact keeps a command's name and first key and hides the rest.
func (e *Engine) Redact(line string) string { return redact(line) }

// checkSize refuses a command that reads a whole value when the value is
// bigger than max elements (or, for strings, stringMax bytes), naming
// what to use instead. It costs one O(1) command when it applies.
func checkSize(rc *conn, args []string, max int64) error {
	name := commandName(args)
	sizer, what, instead := "", "", ""
	switch name {
	case "hgetall", "hkeys", "hvals":
		sizer, what, instead = "HLEN", "fields", "HSCAN, or open it in the key list"
	case "smembers":
		sizer, what, instead = "SCARD", "members", "SSCAN, or open it in the key list"
	case "lrange":
		if len(args) == 4 {
			start, err1 := strconv.ParseInt(args[2], 10, 64)
			stop, err2 := strconv.ParseInt(args[3], 10, 64)
			if err1 == nil && err2 == nil && start >= 0 && stop >= 0 && stop-start < max {
				return nil
			}
		}
		sizer, what, instead = "LLEN", "elements", "LRANGE with a smaller range, or open it in the key list"
	case "lpos":
		sizer, what, instead = "LLEN", "elements", "LRANGE, or open it in the key list"
	case "zrange", "zrevrange", "zrangebyscore", "zrevrangebyscore":
		if name == "zrange" || name == "zrevrange" {
			if len(args) >= 4 {
				start, err1 := strconv.ParseInt(args[2], 10, 64)
				stop, err2 := strconv.ParseInt(args[3], 10, 64)
				byIndex := !slices.ContainsFunc(args, func(a string) bool { return strings.EqualFold(a, "BYSCORE") || strings.EqualFold(a, "BYLEX") })
				if byIndex && err1 == nil && err2 == nil && start >= 0 && stop >= 0 && stop-start < max {
					return nil
				}
			}
		}
		if i := slices.IndexFunc(args, func(a string) bool { return strings.EqualFold(a, "LIMIT") }); i > 0 && i+2 < len(args) {
			if n, err := strconv.ParseInt(args[i+2], 10, 64); err == nil && n >= 0 && n <= max {
				return nil
			}
		}
		sizer, what, instead = "ZCARD", "members", "a smaller range or LIMIT, ZSCAN, or open it in the key list"
	case "xrange", "xrevrange":
		if i := slices.IndexFunc(args, func(a string) bool { return strings.EqualFold(a, "COUNT") }); i > 0 && i+1 < len(args) {
			if n, err := strconv.ParseInt(args[i+1], 10, 64); err == nil && n >= 0 && n <= max {
				return nil
			}
		}
		sizer, what, instead = "XLEN", "entries", "COUNT, or open it in the key list"
	case "hrandfield", "srandmember":
		if len(args) >= 3 {
			n, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil || n > max || n < -max {
				return fmt.Errorf("%w: ask for at most %d", data.ErrInvalid, max)
			}
		}
		return nil
	case "mget":
		if int64(len(args)-1) > max {
			return fmt.Errorf("%w: MGET at most %d keys", data.ErrInvalid, max)
		}
		return nil
	case "get":
		sizer, what, instead = "STRLEN", "bytes", "GETRANGE"
		max = stringMax
	case "hget":
		if len(args) != 3 {
			return nil
		}
		v, err := rc.Do("HSTRLEN", args[1], args[2])
		if err != nil {
			return err
		}
		if v.Int > stringMax {
			return fmt.Errorf("%w: that field holds %d bytes, more than %d; read it in the key list", data.ErrInvalid, v.Int, stringMax)
		}
		return nil
	default:
		return nil
	}
	if len(args) < 2 {
		return nil
	}
	v, err := rc.Do(sizer, args[1])
	if err != nil {
		return err
	}
	if v.Kind == resp.Int && v.Int > max {
		return fmt.Errorf("%w: %s has %d %s, more than %d; use %s", data.ErrInvalid, args[1], v.Int, what, max, instead)
	}
	return nil
}

// Query runs one console command as the reader, in req.DB, and streams
// its reply as rows: pairs for HGETALL-like and WITHSCORES replies,
// [id, fields] for stream entries, [#, value] for other arrays, and one
// cell for a single value. The command must be one of the reader's read
// commands; commands that read a whole value are checked first
// (checkSize). Redis can't time a command out or cancel it, so the
// guards are what bound it; the deadline only bounds Dock's wait.
func (e *Engine) Query(ctx context.Context, t data.Target, c data.Call, req apitypes.DataQueryRequest, send func(apitypes.DataQueryFrame) error) (data.QueryStats, error) {
	start := time.Now()
	stats := data.QueryStats{Outcome: apitypes.OutcomeOK}
	args, err := parseCommand(req.SQL)
	if err != nil {
		return stats, err
	}
	name := commandName(args)
	if !consoleCommands[name] {
		return stats, fmt.Errorf("%w: %s isn't run here: the console runs read commands (GET, HGETALL, SCAN, ZRANGE, XRANGE, TYPE, TTL, ...); changes go through a changeset", data.ErrInvalid, strings.ToUpper(name))
	}
	limit := req.Limit
	if limit <= 0 {
		limit = apitypes.DataQueryLimit
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = apitypes.DataQueryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, min(timeout, apitypes.DataQueryMaxTimeout))
	defer cancel()
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return stats, err
	}
	defer e.release(cn)
	v, err := call(ctx, cn, func(rc *conn) (resp.Value, error) {
		if err := rc.selectDB(req.DB); err != nil {
			return resp.Value{}, err
		}
		if err := checkSize(rc, args, min(int64(limit), wholeMax)); err != nil {
			return resp.Value{}, err
		}
		rc.MaxBulk = apitypes.DataCellMaxBytes + 1
		defer func() { rc.MaxBulk = 0 }()
		return rc.Do(args...)
	})
	stats.Duration = time.Since(start)
	if errors.Is(err, data.ErrInvalid) {
		return stats, err
	}
	if err != nil {
		stats.Outcome = apitypes.OutcomeError
		if ctx.Err() != nil {
			stats.Outcome = apitypes.OutcomeTimeout
		}
		_ = send(apitypes.DataQueryFrame{Type: apitypes.FrameError, Error: &apitypes.DataQueryError{Message: err.Error()}})
		return stats, nil
	}
	if se := v.Err(); se != nil {
		stats.Outcome = apitypes.OutcomeError
		_ = send(apitypes.DataQueryFrame{Type: apitypes.FrameError, Error: &apitypes.DataQueryError{Message: se.Error()}})
		return stats, nil
	}
	cols, rows := replyRows(name, args, v)
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	if err := send(apitypes.DataQueryFrame{Type: apitypes.FrameHeader, Columns: cols}); err != nil {
		stats.Outcome = apitypes.OutcomeCanceled
		return stats, nil
	}
	for len(rows) > 0 {
		n := min(len(rows), apitypes.DataFrameRows)
		if err := send(apitypes.DataQueryFrame{Type: apitypes.FrameRows, Rows: rows[:n]}); err != nil {
			stats.Outcome = apitypes.OutcomeCanceled
			return stats, nil
		}
		stats.Rows += int64(n)
		rows = rows[n:]
	}
	_ = send(apitypes.DataQueryFrame{Type: apitypes.FrameEnd, RowCount: stats.Rows, Truncated: truncated,
		Command: strings.ToUpper(args[0]) + " " + strconv.FormatInt(stats.Rows, 10), DurationMS: stats.Duration.Milliseconds()})
	return stats, nil
}

// pairCommands reply with flat [name, value, ...] arrays.
var pairCommands = map[string]bool{"hgetall": true}

// replyRows turns a reply into columns and rows (see Query).
func replyRows(name string, args []string, v resp.Value) ([]apitypes.DataResultColumn, []apitypes.DataRow) {
	row := func(cells ...[]byte) apitypes.DataRow {
		b := []byte{'['}
		for i, c := range cells {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, c...)
		}
		return append(b, ']')
	}
	withScores := slices.ContainsFunc(args, func(a string) bool { return strings.EqualFold(a, "WITHSCORES") || strings.EqualFold(a, "WITHVALUES") })
	switch {
	case v.Kind != resp.Array:
		return []apitypes.DataResultColumn{col("reply", kindName(v))}, []apitypes.DataRow{row(valueCell(v))}
	case (pairCommands[name] || withScores) && len(v.Arr)%2 == 0:
		second := "value"
		if strings.HasPrefix(name, "z") {
			second = "score"
		}
		var rows []apitypes.DataRow
		for i := 0; i+1 < len(v.Arr); i += 2 {
			rows = append(rows, row(valueCell(v.Arr[i]), valueCell(v.Arr[i+1])))
		}
		return []apitypes.DataResultColumn{col("field", "string"), col(second, "string")}, rows
	case name == "xrange" || name == "xrevrange":
		var rows []apitypes.DataRow
		for _, entry := range v.Arr {
			if len(entry.Arr) == 2 {
				rows = append(rows, row(valueCell(entry.Arr[0]), valueCell(resp.Value{Kind: resp.Array, Arr: entry.Arr[1].Arr})))
			}
		}
		return []apitypes.DataResultColumn{col("id", "string"), col("fields", "json")}, rows
	case strings.HasSuffix(name, "scan") && len(v.Arr) == 2:
		// The cursor first (0 when done), then what the step found.
		rows := []apitypes.DataRow{row([]byte(`"cursor"`), valueCell(v.Arr[0]))}
		for i, x := range v.Arr[1].Arr {
			rows = append(rows, row(strconv.AppendInt(nil, int64(i+1), 10), valueCell(x)))
		}
		return []apitypes.DataResultColumn{col("#", "int"), col("value", "string")}, rows
	}
	rows := make([]apitypes.DataRow, len(v.Arr))
	for i, x := range v.Arr {
		rows[i] = row(strconv.AppendInt(nil, int64(i+1), 10), valueCell(x))
	}
	return []apitypes.DataResultColumn{col("#", "int"), col("value", "string")}, rows
}

// kindName is a reply's type name for a result column.
func kindName(v resp.Value) string {
	switch v.Kind {
	case resp.Int:
		return "integer"
	case resp.Null:
		return "nil"
	case resp.Status:
		return "status"
	}
	return "string"
}

// valueCell is one reply value as a cell: integers as numbers, nil as
// null, strings as in cell, nested arrays as their JSON text.
func valueCell(v resp.Value) []byte {
	switch v.Kind {
	case resp.Int:
		return strconv.AppendInt(nil, v.Int, 10)
	case resp.Null:
		return []byte("null")
	case resp.Array:
		text, _ := json.Marshal(plain(v))
		return cell(string(text), int64(len(text)), apitypes.DataCellMaxBytes)
	}
	return cell(v.Str, v.Len, apitypes.DataCellMaxBytes)
}

// plain is a reply as plain Go values, for JSON text.
func plain(v resp.Value) any {
	switch v.Kind {
	case resp.Int:
		return v.Int
	case resp.Null:
		return nil
	case resp.Array:
		out := make([]any, len(v.Arr))
		for i, x := range v.Arr {
			out[i] = plain(x)
		}
		return out
	}
	return v.Str
}
