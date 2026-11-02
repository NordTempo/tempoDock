package analytics

// Session replay (ADR 0037, phase A3). A sampled page load records itself
// with rrweb and sends what it records in chunks: gzipped JSON arrays of
// rrweb events. The ingest adds what it adds to any event (site, service,
// release, visitor, the browser) and spools the chunk in the server's
// replay spool, apart from the events, so a full spool loses replays
// before page views. The store keeps chunks zstd-compressed in files per
// site and day and indexes them; a replay is the page loads (segments) of
// one visitor with no gap over the session gap, stitched when read.

// ReplayChunk is one chunk of a page load's recording, as the replay
// spool holds it and the store takes it.
type ReplayChunk struct {
	// Time is when the ingest received it (Unix ms).
	Time     int64  `json:"t"`
	Site     int64  `json:"s"`
	Service  int64  `json:"sv"`
	Release  int64  `json:"rl"`
	Hostname string `json:"h"`
	// Path is the page's path when the chunk was sent.
	Path    string `json:"p"`
	Visitor uint64 `json:"v"`
	// Segment is the page load's random ID; Seq numbers its chunks from
	// 0.
	Segment uint64 `json:"g"`
	Seq     uint32 `json:"q"`
	// First and Last are the browser's times of the chunk's first and
	// last rrweb events, Clock its time when it sent the chunk (Unix ms):
	// Time - Clock is how far the browser's clock is behind the server's.
	First int64 `json:"a"`
	Last  int64 `json:"b"`
	Clock int64 `json:"c"`
	// Events counts the chunk's rrweb events.
	Events   int    `json:"n"`
	IPPrefix string `json:"ip,omitempty"`
	Browser  string `json:"br,omitempty"`
	OS       string `json:"o,omitempty"`
	Device   string `json:"d,omitempty"`
	// Data is the gzipped JSON array of rrweb events, as the browser sent
	// it.
	Data []byte `json:"z"`
}

// MaxReplayChunk caps a chunk's gzipped data: with its base64 and the
// rest it stays a line the replay spool takes and a batch the node link
// carries.
const MaxReplayChunk = 384 << 10

// MaxReplayLine caps a replay chunk's spooled line.
const MaxReplayLine = 600 << 10

// ReplaysQuery asks for a site's replays that began in a range, newest
// first. Filters take ReplayDimensions. ErrorsOnly keeps replays whose
// visitor met an error during them.
type ReplaysQuery struct {
	Query
	ErrorsOnly bool `json:"errors_only,omitempty"`
	Limit      int  `json:"limit"`
}

// ReplayDimensions are what replays filter by.
var ReplayDimensions = []Dimension{DimPage, DimCountry, DimBrowser, DimOS, DimDevice, DimRelease}

// MaxReplays caps a ReplaysQuery.
const MaxReplays = 500

// Replay is a visitor's recorded page loads with no gap over the session
// gap: what plays as one. Times are server times (Unix ms).
type Replay struct {
	// ID is its first segment's ID in hex.
	ID       string `json:"id"`
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Segments int    `json:"segments"`
	// EntryPath and ExitPath are the first and last pages recorded.
	EntryPath string `json:"entry_path"`
	ExitPath  string `json:"exit_path"`
	Country   string `json:"country"`
	Browser   string `json:"browser"`
	OS        string `json:"os"`
	Device    string `json:"device"`
	Release   int64  `json:"release"`
	// Errors and Events are the visitor's errors and custom events while
	// it was recorded.
	Errors int64 `json:"errors"`
	Events int64 `json:"events"`
	Bytes  int64 `json:"bytes"`
	// Marks are its page views and errors in order, at most MaxReplayMarks:
	// the shape a list of replays draws. Only lists have them.
	Marks []ReplayMark `json:"marks,omitempty"`
}

// ReplayMark is a page view or an error during a replay, at server time
// T (Unix ms); Name is an error's type.
type ReplayMark struct {
	T    int64  `json:"t"`
	Kind Kind   `json:"k"`
	Name string `json:"n,omitempty"`
}

// MaxReplayMarks caps a listed replay's marks.
const MaxReplayMarks = 50

// ReplayQuery asks for one replay by the ID of any of its segments.
type ReplayQuery struct {
	Site int64  `json:"site"`
	ID   string `json:"id"`
}

// ReplayDetail is a replay with its segments and its timeline: the page
// views, custom events and errors of its visitor while it was recorded.
type ReplayDetail struct {
	Replay
	SegmentList []ReplaySegment `json:"segment_list"`
	Timeline    []TimelineItem  `json:"timeline"`
}

// ReplaySegment is one recorded page load. Start and End are server
// times; Skew is what to add to its rrweb events' times for the server's
// clock.
type ReplaySegment struct {
	ID    string `json:"id"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Path  string `json:"path"`
	Skew  int64  `json:"skew"`
	Bytes int64  `json:"bytes"`
}

// TimelineItem is something on a replay's timeline: a page view, a custom
// event or an error, at server time T (Unix ms).
type TimelineItem struct {
	T    int64  `json:"t"`
	Kind Kind   `json:"kind"`
	Path string `json:"path"`
	// Name is an event's name or an error's type and message.
	Name string `json:"name,omitempty"`
	// Group is an error's group, for its page.
	Group string `json:"group,omitempty"`
}

// MaxTimeline caps a replay's timeline.
const MaxTimeline = 2000

// ReplayPrune deletes a site's replays recorded before Before (Unix ms),
// a day at a time.
type ReplayPrune struct {
	Site   int64 `json:"site"`
	Before int64 `json:"before"`
}

// Replay paths of the store's API.
const (
	// ReplayWritePath takes POST ?source=&position= with ReplayChunk JSON
	// lines, like WritePath, and answers WriteResult.
	ReplayWritePath = "/v1/replay/write"
	// ReplaysPath and ReplayPath take POST with a ReplaysQuery or
	// ReplayQuery and answer []Replay or ReplayDetail.
	ReplaysPath = "/v1/replays"
	ReplayPath  = "/v1/replay"
	// ReplayDataPath answers GET ?site=&segment= with the segment's
	// rrweb events: one JSON array per chunk, a line each, streamed.
	ReplayDataPath = "/v1/replay/data"
	// ReplayPrunePath takes POST with a ReplayPrune and answers
	// PruneResult.
	ReplayPrunePath = "/v1/replay/prune"
)
