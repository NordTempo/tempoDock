package traces

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// hostileSpan is a protobuf Span with IDs and a start, then extra fields.
func hostileSpan(extra []byte) []byte {
	var sp []byte
	sp = protowire.AppendTag(sp, 1, protowire.BytesType)
	sp = protowire.AppendBytes(sp, bytes.Repeat([]byte{0xab}, 16))
	sp = protowire.AppendTag(sp, 2, protowire.BytesType)
	sp = protowire.AppendBytes(sp, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	sp = protowire.AppendTag(sp, 7, protowire.Fixed64Type)
	sp = protowire.AppendFixed64(sp, uint64(time.Unix(1_800_000_000, 0).UnixNano()))
	return append(sp, extra...)
}

// hostileRequest wraps one span in an ExportTraceServiceRequest.
func hostileRequest(span []byte) []byte {
	wrap := func(num protowire.Number, b []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), b)
	}
	return wrap(1, wrap(2, wrap(2, span))) // resource_spans, scope_spans, spans
}

// repeatField is n copies of an empty length-delimited field num.
func repeatField(num protowire.Number, n int) []byte {
	f := protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), nil)
	return bytes.Repeat(f, n)
}

// fanOut is an AnyValue holding lists of width lists, depth deep, of empty
// values: width^depth values in a few bytes each.
func fanOut(width, depth int) []byte {
	v := []byte{} // an empty AnyValue
	for range depth {
		var list []byte
		for range width {
			list = protowire.AppendBytes(protowire.AppendTag(list, 1, protowire.BytesType), v)
		}
		v = protowire.AppendBytes(protowire.AppendTag(nil, 5, protowire.BytesType), list) // array_value
	}
	return v
}

// count is the attributes, events and values sp holds, nested ones
// included.
func count(sp *rawSpan) int {
	var vals func(v value) int
	vals = func(v value) int {
		n := 0
		for _, x := range v.list {
			n += 1 + vals(x)
		}
		for _, a := range v.kv {
			n += 1 + vals(a.val)
		}
		return n
	}
	n := 0
	for _, a := range sp.attrs {
		n += 1 + vals(a.val)
	}
	for _, e := range sp.events {
		n++
		for _, a := range e.attrs {
			n += 1 + vals(a.val)
		}
	}
	return n
}

// A span decodes within the caps however much it sends: attributes,
// events and nested values past them are skipped while decoding, so a
// few KB gzipped can't make the daemon hold hundreds of MB (4 MB of empty
// attributes used to decode to ~250 MB), in protobuf and in JSON.
func TestDecodeCaps(t *testing.T) {
	kv := protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), fanOut(32, 4)) // value: 32^4 nodes
	nested := bytes.Repeat(protowire.AppendBytes(protowire.AppendTag(nil, 9, protowire.BytesType), kv), 4)
	logs := bytes.Repeat(protowire.AppendBytes(protowire.AppendTag(nil, 11, protowire.BytesType),
		protowire.AppendString(protowire.AppendTag(nil, 2, protowire.BytesType), "log")), 100)
	exception := protowire.AppendBytes(protowire.AppendTag(nil, 11, protowire.BytesType),
		protowire.AppendString(protowire.AppendTag(nil, 2, protowire.BytesType), "exception"))
	jsonAttrs := `{"key":"a","value":{}},` + strings.Repeat(`{},`, 1_000_000)
	jsonEvents := strings.Repeat(`{"name":"log"},`, 100) + `{"name":"exception"}`
	jsonSpan := func(extra string) string {
		return `{"resourceSpans":[{"resource":{"attributes":[` + strings.Repeat(`{},`, 100) + `{}]},"scopeSpans":[{"spans":[` +
			`{"traceId":"abababababababababababababababab","spanId":"0102030405060708","startTimeUnixNano":"1800000000000000000",` +
			extra + `}]}]}]}`
	}
	for name, c := range map[string]struct {
		body      []byte
		json      bool
		attrs     int
		exception bool
	}{
		"empty attributes":      {body: hostileRequest(hostileSpan(repeatField(9, 1_500_000))), attrs: maxAttrs},
		"nested lists":          {body: hostileRequest(hostileSpan(nested)), attrs: 4},
		"events":                {body: hostileRequest(hostileSpan(append(logs, exception...))), exception: true},
		"json empty attributes": {body: []byte(jsonSpan(`"attributes":[` + jsonAttrs + `{}]`)), json: true, attrs: maxAttrs},
		"json events":           {body: []byte(jsonSpan(`"events":[` + jsonEvents + `]`)), json: true, exception: true},
	} {
		t.Run(name, func(t *testing.T) {
			var spans int
			add := func(sp *rawSpan) error {
				spans++
				if len(sp.attrs) != c.attrs || len(sp.events) > maxEvents || count(sp) > spanBudget+maxAttrs*(maxEvents+1)+maxEvents {
					t.Errorf("decoded %d attributes, %d events, %d nodes", len(sp.attrs), len(sp.events), count(sp))
				}
				if c.exception && sp.events[len(sp.events)-1].name != "exception" {
					t.Errorf("the exception past the cap wasn't kept")
				}
				return nil
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			var err error
			if c.json {
				err = decodeJSON(c.body, add)
			} else {
				err = decodeProto(c.body, add)
			}
			runtime.ReadMemStats(&after)
			if err != nil || spans != 1 {
				t.Fatalf("%d spans, %v", spans, err)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8<<20 {
				t.Errorf("decoding %d KB allocated %d MB", len(c.body)>>10, alloc>>20)
			}
		})
	}
}

// A service that sends its bodies slowly holds only its own readers: the
// ingest still takes other services' exports meanwhile.
func TestIngestSlowBody(t *testing.T) {
	in, sp := newIngest()
	srv := httptest.NewServer(in)
	defer srv.Close()
	slow := Mint(testKey, testClaims)
	send := func(token string, body io.Reader) (int, error) {
		r, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+TracesPath, body)
		r.Header.Set("Content-Type", "application/x-protobuf")
		r.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(r)
		if err != nil {
			return 0, err
		}
		_ = res.Body.Close()
		return res.StatusCode, nil
	}
	// Two exports from one service that never finish their bodies.
	var writers []*io.PipeWriter
	for range maxServiceReaders {
		pr, pw := io.Pipe()
		writers = append(writers, pw)
		go func() { _, _ = send(slow, pr) }()
	}
	defer func() {
		for _, pw := range writers {
			_ = pw.Close()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		in.readers.mu.Lock()
		n := in.readers.total
		in.readers.mu.Unlock()
		if n == maxServiceReaders {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d bodies being read", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, err := send(slow, strings.NewReader("")); err != nil || code != http.StatusServiceUnavailable {
		t.Errorf("a third export from the slow service: %d %v, want 503", code, err)
	}
	other := testClaims
	other.Service = 99
	body := hostileRequest(hostileSpan(nil))
	if code, err := send(Mint(testKey, other), bytes.NewReader(body)); err != nil || code != http.StatusOK {
		t.Errorf("another service's export: %d %v, want 200", code, err)
	}
	if len(sp.spans(t)) != 1 {
		t.Errorf("spooled %d spans, want 1", len(sp.lines))
	}
}
