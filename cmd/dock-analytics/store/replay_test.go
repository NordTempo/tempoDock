package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// chunk is visitor's chunk seq of segment at day0 plus offset, from a
// browser whose clock is 2 s behind the server's.
func chunk(visitor, segment uint64, seq uint32, offset time.Duration, events string) analytics.ReplayChunk {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(events))
	_ = zw.Close()
	t := day0.Add(offset).UnixMilli()
	return analytics.ReplayChunk{Time: t, Site: 1, Service: 10, Release: 100, Hostname: "shop.example.com", Path: "/p" + string(rune('0'+seq)),
		Visitor: visitor, Segment: segment, Seq: seq, First: t - 2000 - 4000, Last: t - 2000, Clock: t - 2000, Events: 2,
		Browser: "Firefox", OS: "Linux", Device: "desktop", Data: b.Bytes()}
}

// hi is the high bit: random IDs have it half the time, and
// database/sql refuses such uint64 arguments.
const hi = 1 << 63

func TestReplays(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	// Visitor 1: two page loads 10 minutes apart (one replay), and one
	// two hours later (another). Visitor 2: one page load.
	chunks := []analytics.ReplayChunk{
		chunk(hi|1, hi|0xa1, 0, time.Hour, `[{"timestamp":1}]`),
		chunk(hi|1, hi|0xa1, 1, time.Hour+5*time.Second, `[{"timestamp":2}]`),
		chunk(hi|1, hi|0xa2, 0, time.Hour+10*time.Minute, `[{"timestamp":3}]`),
		chunk(hi|1, hi|0xa3, 0, 3*time.Hour, `[{"timestamp":4}]`),
		chunk(2, hi|0xb1, 0, 2*time.Hour, `[{"timestamp":5}]`),
		{Site: 1, Segment: 0xbad, Time: day0.UnixMilli(), Data: []byte("not gzip")},
	}
	n, skipped, err := s.WriteReplay(ctx, "replay:server:1", 1, chunks, nil)
	if err != nil || n != 5 || skipped != 1 {
		t.Fatalf("write: %d written, %d skipped, %v", n, skipped, err)
	}
	// The same batch again is skipped by its cursor.
	if n, _, err := s.WriteReplay(ctx, "replay:server:1", 1, chunks, nil); err != nil || n != 0 {
		t.Fatalf("again: %d, %v", n, err)
	}
	// Visitor 1 met an error and did something during the first replay.
	pv := at(hi|1, time.Hour-5*time.Second, "/p0")
	er := boom(hi|1, time.Hour+time.Minute, hi|7, "boom")
	ev := at(hi|1, time.Hour+2*time.Minute, "/p0")
	ev.Kind, ev.Name = analytics.KindEvent, "signup"
	write(t, s, "server-1", 1, pv, er, ev)

	q := analytics.ReplaysQuery{Query: dayQuery}
	list, err := s.Replays(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != groupHex(hi|0xa3) || list[2].ID != groupHex(hi|0xa1) || list[2].Segments != 2 ||
		list[2].Errors != 1 || list[2].Events != 1 || list[2].EntryPath != "/p0" || list[2].ExitPath != "/p0" || list[2].Browser != "Firefox" {
		t.Fatalf("replays = %+v", list)
	}
	// The chunk's times are in the server's clock: the browser was 2 s
	// behind.
	if want := day0.Add(time.Hour - 4*time.Second).UnixMilli(); list[2].Start != want {
		t.Errorf("start %d, want %d", list[2].Start, want)
	}
	q.ErrorsOnly = true
	if list, err := s.Replays(ctx, q); err != nil || len(list) != 1 {
		t.Errorf("with errors: %+v, %v", list, err)
	}
	q.ErrorsOnly, q.Filters = false, []analytics.Filter{{Dimension: analytics.DimPage, Op: analytics.OpIs, Value: "/p1"}}
	if list, err := s.Replays(ctx, q); err != nil || len(list) != 1 || list[0].ID != groupHex(hi|0xa1) {
		t.Errorf("by page: %+v, %v", list, err)
	}

	// Any segment finds the whole replay.
	d, err := s.Replay(ctx, analytics.ReplayQuery{Site: 1, ID: groupHex(hi | 0xa2)})
	if err != nil {
		t.Fatal(err)
	}
	// The timeline starts with the first page load's own page view.
	if d.ID != groupHex(hi|0xa1) || len(d.SegmentList) != 2 || d.SegmentList[0].Skew != 2000 || len(d.Timeline) != 3 ||
		d.Timeline[0].Kind != analytics.KindPageview || d.Timeline[1].Kind != analytics.KindError ||
		d.Timeline[1].Group != groupHex(hi|7) || d.Timeline[2].Name != "signup" {
		t.Fatalf("replay = %+v", d)
	}
	var out bytes.Buffer
	if err := s.ReplayData(ctx, 1, []string{groupHex(hi | 0xa1), groupHex(hi | 0xa2)}, func() {}, &out); err != nil ||
		out.String() != `{"segment":"`+groupHex(hi|0xa1)+`"}`+"\n"+`[{"timestamp":1}]`+"\n"+`[{"timestamp":2}]`+"\n"+`{"segment":"`+groupHex(hi|0xa2)+`"}`+"\n"+`[{"timestamp":3}]`+"\n" {
		t.Fatalf("data %q, %v", out.String(), err)
	}
	if err := s.ReplayData(ctx, 1, []string{groupHex(0xff)}, func() {}, &out); err == nil {
		t.Error("data of a segment that isn't")
	}

	// Retention: a day's directory and index go.
	day := filepath.Join(s.replayDir(), "1", day0.Format(time.DateOnly))
	if _, err := os.Stat(day); err != nil {
		t.Fatal(err)
	}
	res, err := s.PruneReplays(ctx, analytics.ReplayPrune{Site: 1, Before: day0.Add(24 * time.Hour).UnixMilli()})
	if err != nil || res.Deleted != 5 {
		t.Fatalf("prune: %+v, %v", res, err)
	}
	if _, err := os.Stat(day); !os.IsNotExist(err) {
		t.Errorf("the day's directory: %v", err)
	}
	if _, err := s.Replay(ctx, analytics.ReplayQuery{Site: 1, ID: groupHex(hi | 0xa2)}); err == nil || !strings.Contains(err.Error(), "no replay") {
		t.Errorf("a pruned replay: %v", err)
	}
}
