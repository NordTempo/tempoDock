package redis

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// scanServer is a server that knows what the key browser sends. SCAN's
// cursor is an index into keys and COUNT how many it looks at, like the
// buckets of the real one.
func scanServer(t *testing.T, keys []string) func(context.Context) (net.Conn, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	serve := func(nc net.Conn) {
		defer func() { _ = nc.Close() }()
		r := bufio.NewReader(nc)
		line := func() (string, error) {
			s, err := r.ReadString('\n')
			return strings.TrimRight(s, "\r\n"), err
		}
		for {
			head, err := line()
			if err != nil || !strings.HasPrefix(head, "*") {
				return
			}
			n, _ := strconv.Atoi(head[1:])
			args := make([]string, n)
			for i := range args {
				l, err := line()
				if err != nil {
					return
				}
				size, _ := strconv.Atoi(l[1:])
				buf := make([]byte, size+2)
				if _, err := io.ReadFull(r, buf); err != nil {
					return
				}
				args[i] = string(buf[:size])
			}
			var out string
			switch strings.ToUpper(args[0]) {
			case "HELLO":
				out = "*6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$7\r\nversion\r\n$5\r\n7.4.0\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n"
			case "SCAN":
				at, _ := strconv.Atoi(args[1])
				match, count := "*", 10
				for i := 2; i+1 < len(args); i += 2 {
					switch strings.ToUpper(args[i]) {
					case "MATCH":
						match = args[i+1]
					case "COUNT":
						count, _ = strconv.Atoi(args[i+1])
					}
				}
				end := min(at+count, len(keys))
				var found []string
				for _, k := range keys[at:end] {
					if data.KeyMatch(match, k) {
						found = append(found, k)
					}
				}
				next := strconv.Itoa(end)
				if end == len(keys) {
					next = "0"
				}
				out = fmt.Sprintf("*2\r\n$%d\r\n%s\r\n*%d\r\n", len(next), next, len(found))
				for _, k := range found {
					out += fmt.Sprintf("$%d\r\n%s\r\n", len(k), k)
				}
			case "TYPE":
				out = "+string\r\n"
			case "DBSIZE":
				out = fmt.Sprintf(":%d\r\n", len(keys))
			case "PTTL":
				out = ":-1\r\n"
			case "MEMORY", "STRLEN":
				out = ":64\r\n"
			default: // CLIENT, SELECT
				out = "+OK\r\n"
			}
			if _, err := nc.Write([]byte(out)); err != nil {
				return
			}
		}
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(nc)
		}
	}()
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}
}

// Under a match, a masked call's pages are the same whether a sensitive
// key matches or not: none of them shows it, and a whole scan takes as
// many pages with as many keys. An unmasked call finds the key.
func TestScanKeysMatchPagesDoNotDependOnMaskedKeys(t *testing.T) {
	const sensitive = "session:9f2c07ab"
	var all []string
	for i := range 250 {
		if i == 137 {
			all = append(all, sensitive)
		}
		all = append(all, "user:"+strconv.Itoa(i))
	}
	e := New(Options{})
	defer e.Close()
	tg := data.Target{ServiceID: 1, Database: "0", Dial: scanServer(t, all), Reader: data.Credentials{User: "dock_reader", Password: "pw"}}
	// scan reads every page under match at one key a page.
	scan := func(masked bool, match string) (pages, keys int) {
		c := data.Call{Masked: masked, SensitiveKeys: []string{"session:*"}}
		after := ""
		for {
			page, err := e.ScanKeys(context.Background(), tg, c, apitypes.DataKeysRequest{Match: match, Limit: 1, After: after})
			if err != nil {
				t.Fatal(err)
			}
			pages, keys = pages+1, keys+len(page.Keys)
			if after = page.Next; after == "" {
				return pages, keys
			}
		}
	}
	nonePages, noneKeys := scan(true, "session:0*")
	for _, match := range []string{"session:9*", "session:*", sensitive} {
		if pages, keys := scan(true, match); pages != nonePages || keys != noneKeys {
			t.Errorf("masked, match %q: %d pages with %d keys; a match of nothing takes %d with %d", match, pages, keys, nonePages, noneKeys)
		}
	}
	if _, keys := scan(false, "session:9*"); keys != 1 {
		t.Errorf("unmasked, the key's own match found %d keys", keys)
	}
	// Without a match the key is there, as its pattern.
	if _, keys := scan(true, ""); keys != len(all) {
		t.Errorf("masked, no match: %d keys of %d", keys, len(all))
	}
}
