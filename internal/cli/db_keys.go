package cli

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// newDBKeysCmd lists a key-value database's keys (Redis, ADR 0072).
func newDBKeysCmd(g *globals) *cobra.Command {
	var typ, after string
	var db, limit int
	cmd := dbCmd(g, &cobra.Command{
		Use:   "keys [PATTERN] [--type T] [--db N] [--limit N] [--after CURSOR]",
		Short: "One page of a Redis database's keys; prints the next cursor",
		Long: `Find keys with SCAN, never KEYS: PATTERN is a glob (user:*), --type keeps
one type (string, hash, list, set, zset, stream). A page may hold fewer
keys than --limit and still go on: pass the printed cursor as --after.

Sizes are MEMORY USAGE with 5 samples, an estimate for big values.
With --json, the page as the API returns it, including next.`,
		Example: `  dock db keys 'user:*' --type hash
  dock db keys --db 1 --after eyJ...`,
		Args: cobra.MaximumNArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		q := url.Values{}
		if len(args) == 1 {
			q.Set("match", args[0])
		}
		if typ != "" {
			q.Set("type", typ)
		}
		if after != "" {
			q.Set("after", after)
		}
		if db != 0 {
			q.Set("db", strconv.Itoa(db))
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		page, err := get[apitypes.DataKeyPage](ctx, s, data+"/keys?"+q.Encode())
		if err != nil {
			return err
		}
		return g.io.Print(page, func() error {
			t := g.io.Table("KEY", "TYPE", "LENGTH", "SIZE", "TTL")
			for _, k := range page.Keys {
				name := k.Key
				switch {
				case k.Masked:
					name += " (masked)"
				case k.Hex:
					name = "hex:" + name
				}
				t.Row(cut(name, maxCellWidth), k.Type, itoa(k.Length), bytesize(k.Bytes), ttlText(k.TTLMs))
			}
			if err := t.Flush(); err != nil {
				return err
			}
			g.io.Progressf("%s of %s in db %d", plural(len(page.Keys), "key"), itoa(page.Total), db)
			if page.Next != "" {
				g.io.Progressf("next page: --after %s", page.Next)
			}
			return nil
		})
	})
	cmd.Flags().StringVar(&typ, "type", "", "keep one type: string, hash, list, set, zset, stream")
	cmd.Flags().IntVar(&db, "db", 0, "logical database")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, fmt.Sprintf("keys per page (default %d, at most %d)", apitypes.DataKeysLimit, apitypes.DataKeysMaxLimit))
	cmd.Flags().StringVar(&after, "after", "", "cursor of the previous page")
	return cmd
}

// newDBKeyCmd prints one key and a page of its value.
func newDBKeyCmd(g *globals) *cobra.Command {
	var after string
	var db, limit int
	var isHex, full, reveal bool
	cmd := dbCmd(g, &cobra.Command{
		Use:   "key KEY [--db N] [--limit N] [--after CURSOR] [--full]",
		Short: "One Redis key: its type, TTL and size, and a page of its value",
		Long: `Print a key's type, TTL, length and sampled size, then one page of its value:
a string, a hash's fields, a list's elements, a set's members, a sorted
set's members and scores, or a stream's entries (newest first). Values
over 8 KB are cut unless --full (up to 1 MB). Pass the printed cursor as
--after for the next page. --hex reads KEY as hex, for names that aren't
UTF-8 (dock db keys shows them as hex:...). A key matching a sensitive
pattern needs --reveal, if your access lets you see sensitive values;
each reveal is audited.`,
		Example: `  dock db key user:1042
  dock db key queue:emails --limit 500`,
		Args: cobra.ExactArgs(1),
	}, func(ctx context.Context, s *session, _ target, data string, args []string) error {
		q := url.Values{"key": {args[0]}}
		if isHex {
			q.Set("hex", "true")
		}
		if after != "" {
			q.Set("after", after)
		}
		if db != 0 {
			q.Set("db", strconv.Itoa(db))
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		if full {
			q.Set("full", "true")
		}
		if reveal {
			q.Set("reveal", "true")
		}
		v, err := get[apitypes.DataKeyValue](ctx, s, data+"/keys/value?"+q.Encode())
		if err != nil {
			return err
		}
		return g.io.Print(v, func() error {
			g.io.Progressf("%s: %s, length %s, %s, TTL %s", v.Key, v.Type, itoa(v.Length), bytesize(v.Bytes), ttlText(v.TTLMs))
			if len(v.Columns) == 0 {
				g.io.Progressf("a module type: its value isn't shown")
				return nil
			}
			names := make([]string, len(v.Columns))
			for i, c := range v.Columns {
				names[i] = c.Name
			}
			t := g.io.Table(names...)
			for _, r := range v.Rows {
				cells, err := rowCells(r, full)
				if err != nil {
					return err
				}
				if !full {
					for i := range cells {
						cells[i] = cut(cells[i], maxCellWidth)
					}
				}
				t.Row(cells...)
			}
			if err := t.Flush(); err != nil {
				return err
			}
			if v.Next != "" {
				g.io.Progressf("next page: --after %s", v.Next)
			}
			return nil
		})
	})
	cmd.Flags().IntVar(&db, "db", 0, "logical database")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, fmt.Sprintf("items per page (default %d, at most %d)", apitypes.DataKeyValueLimit, apitypes.DataKeyValueMaxLimit))
	cmd.Flags().StringVar(&after, "after", "", "cursor of the previous page")
	cmd.Flags().BoolVar(&isHex, "hex", false, "KEY is hex")
	cmd.Flags().BoolVar(&full, "full", false, "values untruncated, up to 1 MB")
	cmd.Flags().BoolVar(&reveal, "reveal", false, "read a key matching a sensitive pattern (needs sensitive visible; audited)")
	return cmd
}

// ttlText is a key's TTL in words: "none", "28s", "23h".
func ttlText(ms int64) string {
	if ms < 0 {
		return "none"
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}
