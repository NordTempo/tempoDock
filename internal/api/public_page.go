package api

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// A public dashboard's page (ADR 0037, phase A4; Saile's pick: A): one
// column with the site's headline numbers, visitors per day, pages,
// sources and countries, and its error groups if it shows them. Rendered
// here, once per dashboard and range a minute (the cache in
// public_stats.go), so a visitor's browser gets a few kilobytes of HTML
// and one line of script that names countries.

//go:embed public_page.html
var publicPageSource string

// publicPage is the page's template.
var publicPage = template.Must(template.New("public").Parse(publicPageSource))

// publicScript names countries in the reader's browser, which knows them
// all ("NO" is Norway), so Dock carries no table of names.
const publicScript = `for(const e of document.querySelectorAll("[data-cc]"))try{e.textContent=new Intl.DisplayNames(["en"],{type:"region"}).of(e.dataset.cc)}catch{}`

// publicCSP lets the page run its one script and inline styles, and
// nothing else: no requests, frames or forms.
var publicCSP = func() string {
	sum := sha256.Sum256([]byte(publicScript))
	return "default-src 'none'; style-src 'unsafe-inline'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

// pageData is what the template shows.
type pageData struct {
	Site    string
	Ranges  []pageRange
	Offline bool
	Live    string
	KPIs    []pageKPI
	Line    string
	Area    string
	Peak    string
	First   string
	Last    string
	Tables  []pageTable
	Updated string
	Script  template.JS
}

type pageRange struct {
	Name string
	On   bool
}

type pageKPI struct{ Name, Value, Delta, Tone string }

type pageTable struct {
	Name, Unit string
	Rows       []pageRow
}

type pageRow struct {
	Value, Count string
	// Country is the ISO code the page's script names; Mono sets the
	// value in monospace (paths).
	Country string
	Mono    bool
	Share   int
}

// renderPublic renders a dashboard's page.
func renderPublic(st apitypes.PublicStats) ([]byte, error) {
	d := pageData{Site: st.Site, Offline: !st.StoreOnline, Script: template.JS(publicScript)}
	for _, r := range core.PublicRanges {
		d.Ranges = append(d.Ranges, pageRange{r, r == st.Range})
	}
	if st.StoreOnline {
		d.Updated = st.GeneratedAt.UTC().Format("15:04 UTC")
		switch st.LiveVisitors {
		case 0:
			d.Live = "No one on the site now"
		case 1:
			d.Live = "1 person on the site now"
		default:
			d.Live = grouped(int64(st.LiveVisitors)) + " people on the site now"
		}
		o, p := st.Overview, st.Previous
		d.KPIs = []pageKPI{
			withDelta("Visitors", o.Visitors, p.Visitors),
			withDelta("Page views", o.Pageviews, p.Pageviews),
			{Name: "Bounce rate", Value: strconv.Itoa(int(math.Round(o.BounceRate*100))) + "%"},
			{Name: "Visit length", Value: visitLength(o.DurationMS)},
		}
		d.Line, d.Area, d.Peak = chart(st.Series, st.From, st.To)
		d.First = time.UnixMilli(st.From).UTC().Format("Jan 2")
		d.Last = time.UnixMilli(st.To).UTC().Format("Jan 2")
		d.Tables = []pageTable{
			table("Pages", "Visitors", st.Breakdowns["page"], func(r *pageRow) { r.Mono = true }),
			table("Sources", "Visitors", st.Breakdowns["referrer"], func(r *pageRow) {
				if r.Value == "" {
					r.Value = "Direct"
				}
			}),
			table("Countries", "Visitors", st.Breakdowns["country"], func(r *pageRow) {
				if r.Value == "" {
					r.Value = "Unknown"
				} else {
					r.Country = r.Value
				}
			}),
		}
		if st.ShowErrors {
			rows := make([]apitypes.PublicRow, 0, len(st.Errors))
			for _, e := range st.Errors {
				msg := e.Message
				if e.Type != "" && !strings.HasPrefix(msg, e.Type) {
					msg = e.Type + ": " + msg
				}
				rows = append(rows, apitypes.PublicRow{Value: msg, Visitors: e.Visitors})
			}
			d.Tables = append(d.Tables, table("Errors", "Visitors", rows, nil))
		}
	}
	var b bytes.Buffer
	err := publicPage.Execute(&b, d)
	return b.Bytes(), err
}

// withDelta is a headline number with its change on the period before.
func withDelta(name string, now, before int64) pageKPI {
	k := pageKPI{Name: name, Value: grouped(now)}
	if before > 0 {
		pct := math.Round(float64(now-before) / float64(before) * 100)
		k.Delta, k.Tone = fmt.Sprintf("%+.0f%%", pct), "ok"
		if pct < 0 {
			k.Delta, k.Tone = "−"+k.Delta[1:], "fail"
		}
	}
	return k
}

// table is a breakdown's rows, each with its share of the first's.
func table(name, unit string, rows []apitypes.PublicRow, tweak func(*pageRow)) pageTable {
	t := pageTable{Name: name, Unit: unit}
	var top int64
	for _, r := range rows {
		top = max(top, r.Visitors)
	}
	for _, r := range rows {
		row := pageRow{Value: r.Value, Count: grouped(r.Visitors)}
		if top > 0 {
			row.Share = int(r.Visitors * 100 / top)
		}
		if tweak != nil {
			tweak(&row)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// chart draws visitors per day in a 700×150 box: the line, the area under
// it, and the highest day. Days without visits are zero.
func chart(pts []apitypes.AnalyticsPoint, from, to int64) (line, area, peak string) {
	const w, h, day = 700.0, 150.0, int64(24 * time.Hour / time.Millisecond)
	start := from - from%day
	n := int((to-start)/day) + 1
	vals := make([]int64, n)
	var top int64
	for _, p := range pts {
		if i := int((p.T - start) / day); i >= 0 && i < n {
			vals[i] += p.Visitors
			top = max(top, vals[i])
		}
	}
	scale := h * 0.92 / float64(max(top, 1))
	var b strings.Builder
	for i, v := range vals {
		x := w * float64(i) / float64(max(n-1, 1))
		y := h - float64(v)*scale
		if i == 0 {
			b.WriteString("M")
		} else {
			b.WriteString(" L")
		}
		fmt.Fprintf(&b, "%.1f %.1f", x, y)
	}
	line = b.String()
	return line, line + fmt.Sprintf(" L%.0f %.0f L0 %.0f Z", w, h, h), grouped(top)
}

// grouped is n with thousands separators: 18,420.
func grouped(n int64) string {
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

// visitLength is a visit's average length: 2m 48s.
func visitLength(ms int64) string {
	s := (ms + 500) / 1000
	if s < 60 {
		return strconv.FormatInt(s, 10) + "s"
	}
	return fmt.Sprintf("%dm %02ds", s/60, s%60)
}
