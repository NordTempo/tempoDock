package ingest

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// serveReplay takes one chunk of a page load's recording (phase A3): the
// gzipped rrweb events as the body, the rest as query parameters (g the
// segment, q its sequence number, a and b the first and last events'
// times, c the browser's clock, n the events, u the page). Like events it
// answers 202 whether or not it keeps the chunk; a site that doesn't
// record, a bot or a day without salt keeps nothing.
func (in *Ingest) serveReplay(w http.ResponseWriter, r *http.Request) {
	body := bufs.Get().(*bytes.Buffer)
	defer bufs.Put(body)
	ch, keep, ok := in.replayChunk(r, body)
	if !ok {
		in.reject(w)
		return
	}
	if !keep {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	line, err := json.Marshal(&ch)
	if err != nil {
		in.reject(w)
		return
	}
	if err := in.Replay.Append(line); err != nil {
		if in.Log != nil {
			in.Log.Error("analytics replay spool", "err", err)
		}
		http.Error(w, "the recording couldn't be kept", http.StatusServiceUnavailable)
		return
	}
	in.stats.Replays.Add(1)
	w.WriteHeader(http.StatusAccepted)
}

// replayChunk reads a chunk's request, its body into body, which the
// chunk's Data then refers to. ok is false for one the recorder wouldn't
// send; keep is false for one that isn't kept.
func (in *Ingest) replayChunk(r *http.Request, body *bytes.Buffer) (ch analytics.ReplayChunk, keep, ok bool) {
	site, service, release, ok := parseTag(r.Header.Get(TagHeader))
	if !ok || in.Replay == nil {
		return ch, false, ok
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
		len(q.Get("g")) != 16 || last < first || events < 1 {
		return ch, false, false
	}
	host := hostOnly(r.Host)
	if !strings.EqualFold(hostOnly(u.Host), host) {
		return ch, false, false
	}
	body.Reset()
	if _, err := body.ReadFrom(io.LimitReader(r.Body, analytics.MaxReplayChunk+1)); err != nil ||
		body.Len() > analytics.MaxReplayChunk || !bytes.HasPrefix(body.Bytes(), []byte{0x1f, 0x8b}) {
		return ch, false, false
	}
	if percent, _ := in.Rules.replay(site); percent == 0 {
		in.stats.Excluded.Add(1) // turned off; a cached s.js may still load the recorder
		return ch, false, true
	}
	agent := ParseAgent(r.Header.Get("User-Agent"))
	if agent.Bot {
		in.stats.Bots.Add(1)
		return ch, false, true
	}
	now := time.Now
	if in.Now != nil {
		now = in.Now
	}
	t := now()
	salt, ok := in.Salts.get(Day(t))
	if !ok {
		in.stats.NoSalt.Add(1)
		return ch, false, true
	}
	ip := r.Header.Get(ClientIPHeader)
	path := cut(cmpStr(u.EscapedPath(), "/"), maxPath)
	if in.Rules.skip(site, path, ip) {
		in.stats.Excluded.Add(1)
		return ch, false, true
	}
	return analytics.ReplayChunk{
		Time: t.UnixMilli(), Site: site, Service: service, Release: release, Hostname: host, Path: path,
		Visitor: visitorID(salt, site, ip, r.Header.Get("User-Agent")), Segment: segment, Seq: uint32(seq),
		First: first, Last: last, Clock: clock, Events: events, IPPrefix: ipPrefix(ip),
		Browser: agent.Browser, OS: agent.OS, Device: agent.Device, Data: body.Bytes(),
	}, true, true
}
