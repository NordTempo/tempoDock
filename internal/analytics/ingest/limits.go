package ingest

import (
	"encoding/binary"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"
)

// Rate limits. Caddy forwards any visitor's requests here, so a script or
// a flood that passes for a browser mustn't fill the spools, the store or
// the daemon's memory. The limits count what was kept in the current
// second, in a fixed-size table per address and an entry per site. A
// window starts lazily, when a request reads the clock in a new second:
// there is no timer.
//
// The limits are far above what real pages send, so they only ever cut a
// flood. Over them, an event is answered 202 like any event that isn't
// kept: the script never retries, so a 429 would only tell a flood where
// the limit is and show as an error in a visitor's console. A replay
// chunk is answered 429: the recorder drops it (it doesn't look at the
// status), and anything else sending chunks learns to back off.
const (
	// eventsPerIP: a page load sends a page view, its vitals, at most 10
	// errors, outbound clicks and the site's own events, a handful in its
	// busiest second. 50 leaves room for an office's or a mobile
	// carrier's NAT behind one address.
	eventsPerIP = 50
	// replaysPerIP: the recorder sends a chunk every 5 s, sooner at 1,000
	// rrweb events, so a recording page load sends 0.2 to 1 a second.
	replaysPerIP = 5
	// eventBytesPerSite: about 7,000 events a second at 300 bytes each,
	// over half a billion a day: far beyond any site a Dock server hosts.
	eventBytesPerSite = 2 << 20
	// replayBytesPerSite: a chunk is typically 10-50 KB every 5 s, so
	// 4 MB a second is several hundred page loads recording at once. It
	// is over analytics.MaxReplayChunk, so a full chunk always fits an
	// empty window.
	replayBytesPerSite = 4 << 20

	// ipSlots is the size of the address table: 16 KB. Addresses that
	// hash to the same slot share one limit, which at these limits only
	// matters to a flood.
	ipSlots = 2048
	// maxSites caps the sites the limiter keeps an entry for; past it
	// they are all forgotten. Sites come from Caddy's routing table, so a
	// server has far fewer.
	maxSites = 1024
)

// limiter holds the ingest's rate limits. Its zero value is ready to use,
// and it is safe for concurrent use: one short lock per request.
type limiter struct {
	mu sync.Mutex
	// seed keys the address table's hash, so a visitor can't pick
	// addresses that share another's slot. Set on first use.
	seed  uint64
	ips   [ipSlots]ipUse
	sites map[int64]siteUse
}

// ipUse counts what the addresses of one slot had kept in a second.
type ipUse struct {
	second          uint32
	events, replays uint16
}

// siteUse counts the bytes of a site's events and replay chunks kept in
// a second.
type siteUse struct {
	second          uint32
	events, replays int64
}

// allow reports whether one more event (or, with replay, replay chunk) of
// n bytes from ip to site is within the limits for the second t is in,
// and counts it if so.
func (l *limiter) allow(t time.Time, replay bool, site int64, ip string, n int64) bool {
	key := addrKey(ip)
	sec := uint32(t.Unix())
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seed == 0 {
		l.seed = rand.Uint64() | 1
	}
	u := &l.ips[slot(key, l.seed)]
	if u.second != sec {
		*u = ipUse{second: sec}
	}
	s, known := l.sites[site]
	if s.second != sec {
		s = siteUse{second: sec}
	}
	count, perIP, bytes, perSite := &u.events, uint16(eventsPerIP), &s.events, int64(eventBytesPerSite)
	if replay {
		count, perIP, bytes, perSite = &u.replays, replaysPerIP, &s.replays, replayBytesPerSite
	}
	if *count >= perIP || *bytes+n > perSite {
		return false
	}
	*count++
	*bytes += n
	switch {
	case l.sites == nil:
		l.sites = make(map[int64]siteUse)
	case !known && len(l.sites) >= maxSites:
		clear(l.sites)
	}
	l.sites[site] = s
	return true
}

// addrKey is what a client address is counted by: an IPv4 address, or
// an IPv6 address's /64, which one visitor holds whole. An address that
// doesn't parse is 0.
func addrKey(ip string) uint64 {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return 0
	}
	a = a.Unmap()
	b := a.As16()
	if a.Is4() {
		return uint64(binary.BigEndian.Uint32(b[12:]))
	}
	return binary.BigEndian.Uint64(b[:8])
}

// slot is an address key's slot in the table: splitmix64's finalizer over
// the key and seed.
func slot(key, seed uint64) int {
	k := key ^ seed
	k = (k ^ k>>30) * 0xbf58476d1ce4e5b9
	k = (k ^ k>>27) * 0x94d049bb133111eb
	return int((k ^ k>>31) % ipSlots)
}
