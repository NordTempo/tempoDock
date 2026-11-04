package ingest

import (
	"bytes"
	"encoding/base64"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// maxReplays caps the replay chunks in flight: past it a chunk is
// answered 503 before its body is read. A chunk holds its body (up to
// analytics.MaxReplayChunk, 384 KB) and its spooled line (the body in
// base64 and the rest, up to 512 KB), under 900 KB together, so four
// hold 3.5 MB at most, a twentieth of the daemon's GOMEMLIMIT of 64 MiB.
// Without the cap 50 concurrent uploads went past it. Typical chunks are
// 10-50 KB and take milliseconds, so four slots are rarely all taken;
// when slow uploads take them, replays drop and events don't.
const maxReplays = 4

// replayBufs are reused for replay chunks' bodies and spooled lines, at
// most 2*maxReplays in use at once: apart from bufs, so the event path's
// pool never holds buffers of hundreds of KB.
var replayBufs = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// serveReplay takes one chunk of a page load's recording (phase A3): the
// gzipped rrweb events as the body, the rest as query parameters (g the
// segment, q its sequence number, a and b the first and last events'
// times, c the browser's clock, n the events, u the page). Like events it
// answers 202 whether or not it keeps the chunk; a site that doesn't
// record, a bot or a day without salt keeps nothing, and its body isn't
// read. Over the rate limits it answers 429, and with maxReplays chunks
// in flight 503; the recorder drops the chunk either way.
func (in *Ingest) serveReplay(w http.ResponseWriter, r *http.Request) {
	ch, status := in.replayChunk(r)
	if status == http.StatusBadRequest {
		in.reject(w)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if in.replaying.Add(1) > maxReplays {
		in.replaying.Add(-1)
		in.stats.Limited.Add(1)
		http.Error(w, "too many recordings at once; try again later", http.StatusServiceUnavailable)
		return
	}
	defer in.replaying.Add(-1)

	body := replayBufs.Get().(*bytes.Buffer)
	defer replayBufs.Put(body)
	body.Reset()
	if r.ContentLength > 0 {
		// The whole body and ReadFrom's last read, so the buffer
		// doesn't grow while it reads.
		body.Grow(int(r.ContentLength) + bytes.MinRead)
	}
	if _, err := body.ReadFrom(io.LimitReader(r.Body, analytics.MaxReplayChunk+1)); err != nil ||
		body.Len() > analytics.MaxReplayChunk || !bytes.HasPrefix(body.Bytes(), []byte{0x1f, 0x8b}) {
		in.reject(w)
		return
	}
	ch.Data = body.Bytes()

	line := replayBufs.Get().(*bytes.Buffer)
	defer replayBufs.Put(line)
	line.Reset()
	line.Grow(base64.StdEncoding.EncodedLen(body.Len()) + 1<<10)
	if err := json.MarshalWrite(line, &ch); err != nil {
		in.reject(w)
		return
	}
	if err := in.Replay.Append(line.Bytes()); err != nil {
		if in.Log != nil {
			in.Log.Error("analytics replay spool", "err", err)
		}
		http.Error(w, "the recording couldn't be kept", http.StatusServiceUnavailable)
		return
	}
	in.stats.Replays.Add(1)
	w.WriteHeader(http.StatusAccepted)
}

// replayChunk checks a chunk's request without reading its body and
// fills in all of the chunk but its Data. status is 0 for a chunk to
// keep, otherwise the answer: 400 for one the recorder wouldn't send,
// 202 for one that isn't kept, 429 for one over the rate limits.
func (in *Ingest) replayChunk(r *http.Request) (ch analytics.ReplayChunk, status int) {
	site, service, release, ok := parseTag(r.Header.Get(TagHeader))
	if !ok {
		return ch, http.StatusBadRequest
	}
	if in.Replay == nil {
		return ch, http.StatusAccepted
	}
	q := r.URL.Query()
	segment, err1 := strconv.ParseUint(q.Get("g"), 16, 64)
	seq, err2 := strconv.ParseUint(q.Get("q"), 10, 32)
	first, err3 := strconv.ParseInt(q.Get("a"), 10, 64)
	last, err4 := strconv.ParseInt(q.Get("b"), 10, 64)
	clock, err5 := strconv.ParseInt(q.Get("c"), 10, 64)
	events, err6 := strconv.Atoi(q.Get("n"))
	u, err7 := url.Parse(q.Get("u"))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil || err7 != nil ||
		len(q.Get("g")) != 16 || last < first || events < 1 || r.ContentLength > analytics.MaxReplayChunk {
		return ch, http.StatusBadRequest
	}
	host := hostOnly(r.Host)
	if !strings.EqualFold(hostOnly(u.Host), host) {
		return ch, http.StatusBadRequest
	}
	if percent, _ := in.Rules.replay(site); percent == 0 {
		in.stats.Excluded.Add(1) // turned off; a cached s.js may still load the recorder
		return ch, http.StatusAccepted
	}
	agent := ParseAgent(r.Header.Get("User-Agent"))
	if agent.Bot {
		in.stats.Bots.Add(1)
		return ch, http.StatusAccepted
	}
	t := in.now()
	salt, ok := in.Salts.get(Day(t))
	if !ok {
		in.stats.NoSalt.Add(1)
		return ch, http.StatusAccepted
	}
	ip := r.Header.Get(ClientIPHeader)
	path := cut(cmpStr(u.EscapedPath(), "/"), maxPath)
	if in.Rules.skip(site, path, ip) {
		in.stats.Excluded.Add(1)
		return ch, http.StatusAccepted
	}
	// A body of unknown length counts as the most it may be.
	size := r.ContentLength
	if size < 0 {
		size = analytics.MaxReplayChunk
	}
	if !in.limits.allow(t, true, site, ip, size) {
		in.stats.Limited.Add(1)
		return ch, http.StatusTooManyRequests
	}
	return analytics.ReplayChunk{
		Time: t.UnixMilli(), Site: site, Service: service, Release: release, Hostname: host, Path: path,
		Visitor: visitorID(salt, site, ip, r.Header.Get("User-Agent")), Segment: segment, Seq: uint32(seq),
		First: first, Last: last, Clock: clock, Events: events, IPPrefix: ipPrefix(ip),
		Browser: agent.Browser, OS: agent.OS, Device: agent.Device,
	}, 0
}
