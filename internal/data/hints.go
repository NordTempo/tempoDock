package data

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Insight hints (ADR 0069) every engine derives the same way: connection
// pressure and what sessions do. Engines add their own (vacuum, buffer
// pool, ...) with Hints.Add. Words name things and give numbers, never
// values from the data.

// Thresholds of the shared hints. Notes are worth knowing, warnings worth
// doing something about soon.
const (
	connectionsNote = 0.80
	connectionsWarn = 0.95
	idleInTxWarn    = time.Minute
	blockedWarn     = 5 * time.Second
	longQueryNote   = time.Minute
	longQueryWarn   = 10 * time.Minute
)

// Hints collects what needs a look. The zero Hints is empty and ready.
type Hints struct{ list []apitypes.DataHint }

// Add records one hint: level "warn" or "note", the view it belongs to,
// its kind and subject, a short title, the evidence in words and a
// statement to copy (or "").
func (h *Hints) Add(level string, view apitypes.DataInsightView, kind apitypes.DataHintKind, subject, title, detail, copy string) {
	h.list = append(h.list, apitypes.DataHint{Kind: kind, Level: level, View: view, Subject: subject, Title: title, Detail: detail, Copy: copy})
}

// Sorted returns the hints, warnings first, then in view order (stable
// within each).
func (h *Hints) Sorted() []apitypes.DataHint {
	order := map[apitypes.DataInsightView]int{}
	for i, v := range apitypes.DataInsightViews {
		order[v] = i
	}
	rank := func(l string) int {
		if l == "warn" {
			return 0
		}
		return 1
	}
	out := append([]apitypes.DataHint{}, h.list...)
	slices.SortStableFunc(out, func(a, b apitypes.DataHint) int {
		return cmp.Or(cmp.Compare(rank(a.Level), rank(b.Level)), cmp.Compare(order[a.View], order[b.View]))
	})
	return out
}

// Connections flags a database close to (or at) its connection limit.
func (h *Hints) Connections(p apitypes.DataPressure) {
	usable := p.Max - p.Reserved
	switch {
	case p.Full:
		h.Add("warn", apitypes.InsightOverview, apitypes.HintConnectionsHigh, "", "Every connection is taken",
			"the database refused a new connection; restarting it frees them", "")
	case usable > 0 && float64(p.Used) >= connectionsWarn*float64(usable):
		h.Add("warn", apitypes.InsightOverview, apitypes.HintConnectionsHigh, "", "Almost out of connections",
			fmt.Sprintf("%d of %d usable connections in use, %d idle", p.Used, usable, p.Idle), "")
	case usable > 0 && float64(p.Used) >= connectionsNote*float64(usable):
		h.Add("note", apitypes.InsightOverview, apitypes.HintConnectionsHigh, "", "Connections running high",
			fmt.Sprintf("%d of %d usable connections in use, %d idle", p.Used, usable, p.Idle), "")
	}
}

// SessionWords are an engine's words in session hints.
type SessionWords struct {
	// ID names DataSession.PID: "pid", "connection".
	ID string
	// Cleanup is what an open transaction holds back: "vacuum", "purge".
	Cleanup string
}

// Sessions flags idle transactions, the roots of lock chains and long
// queries among client sessions (Dock's own and background ones are left
// out).
func (h *Hints) Sessions(sessions []apitypes.DataSession, now time.Time, words SessionWords) {
	blocks := map[string]int{}
	for _, s := range sessions {
		if s.QueryAt != nil && now.Sub(*s.QueryAt) >= blockedWarn {
			for _, b := range s.BlockedBy {
				blocks[b]++
			}
		}
	}
	who := func(s apitypes.DataSession) string {
		return fmt.Sprintf("%s (%s), %s %d", cmp.Or(s.Application, "no application name"), s.User, words.ID, s.PID)
	}
	for _, s := range sessions {
		if s.Kind != "client" {
			continue
		}
		n := blocks[s.ID]
		blocking := ""
		if n > 0 {
			blocking = fmt.Sprintf("; blocks %d %s", n, Plural(n, "session", "sessions"))
		}
		switch {
		case s.State == "idle_in_transaction" && s.StateAt != nil && now.Sub(*s.StateAt) >= idleInTxWarn:
			h.Add("warn", apitypes.InsightSessions, apitypes.HintIdleInTransaction, s.ID, "Idle in transaction "+DurText(now.Sub(*s.StateAt)),
				who(s)+"; holds its locks and holds back "+words.Cleanup+blocking, "")
		case n > 0 && len(s.BlockedBy) == 0:
			h.Add("warn", apitypes.InsightSessions, apitypes.HintBlocked, s.ID, fmt.Sprintf("Blocking %d %s", n, Plural(n, "session", "sessions")),
				who(s)+" holds a lock others wait for", "")
		case s.State == "active" && len(s.BlockedBy) == 0 && s.QueryAt != nil && now.Sub(*s.QueryAt) >= longQueryNote:
			level := "note"
			if now.Sub(*s.QueryAt) >= longQueryWarn {
				level = "warn"
			}
			h.Add(level, apitypes.InsightSessions, apitypes.HintLongQuery, s.ID, "Running for "+DurText(now.Sub(*s.QueryAt)), who(s)+blocking, "")
		}
	}
}

// Plural picks one or many by n.
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// DurText is a duration as "6m 12s", "3d 4h".
func DurText(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// AgoText is "3d 4h ago", or "never".
func AgoText(t *time.Time, now time.Time) string {
	if t == nil {
		return "never"
	}
	return DurText(now.Sub(*t)) + " ago"
}

// Count is a number with thousands separators: 12,431.
func Count(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// BytesText is a size in KB, MB or GB.
func BytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
}
