package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Small formatting helpers for human output.

// bytesize renders n as "512 B", "1.2 MB" and so on (powers of 1024).
func bytesize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ago renders how long ago t was, coarsely: "now", "42s ago", "3m ago",
// "5h ago", "12d ago".
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// agoPtr is ago for optional times, "-" when unset.
func agoPtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return ago(*t)
}

// dash shows "-" for an empty cell, so columns stay readable.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// shortID shortens an image ID like "sha256:4be1c0…" for tables.
func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// plural is "1 change" or "2 changes".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// jsonLine writes v as one compact JSON line, for streaming commands
// (`dock logs --json`) that print one object per line.
func jsonLine(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
