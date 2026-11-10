package api

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A public status page (ADR 0073; Saile's pick: mock A, the ledger): one
// column, what is wrong first, then each component with its status and a
// bar of its last 90 days, dark green when fine so a bad day is what the
// eye finds. System fonts, no script, no requests: the CSP allows only
// the page's own style.

//go:embed status_page.html
var statusPageSource string

// statusPageTmpl is the page's template.
var statusPageTmpl = template.Must(template.New("status").Parse(statusPageSource))

// statusCSS is the page's style, allowed by its hash.
const statusCSS = `*{box-sizing:border-box}
body{margin:0;background:#000;color:#fafafa;font:14px/1.45 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Inter,Roboto,sans-serif;font-variant-numeric:tabular-nums}
main{max-width:720px;margin:0 auto;padding:28px 20px 32px}
header{display:flex;align-items:baseline;gap:12px}
h1{font-size:15px;font-weight:600;margin:0;flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
header a{font-size:12px;color:#8b8b94}
.state{display:flex;align-items:baseline;flex-wrap:wrap;gap:0 12px;font-size:20px;font-weight:600;margin:18px 0 6px}
.upd{font-size:13px;font-weight:400;color:#8b8b94}
.dot{display:inline-block;width:10px;height:10px;border-radius:999px;align-self:center}
.dot.operational{background:#4ade80}.dot.degraded{background:#fbbf24}.dot.partial_outage{background:#fb923c}.dot.major_outage{background:#f87171}
h2{font-size:15px;font-weight:600;margin:24px 0 2px}
section{padding:9px 0 10px;border-bottom:1px solid #161618}
.row{display:flex;justify-content:space-between;gap:12px;margin-bottom:6px}
.name{min-width:0}
.desc{color:#8b8b94;font-size:13px}
.operational{color:#4ade80}.idle{color:#8b8b94}.degraded{color:#fbbf24}.partial_outage{color:#fb923c}.major_outage{color:#f87171}
.bar{display:flex;gap:2px;height:28px}
.bar i{flex:1;min-width:1px;border-radius:1px;background:#1c1c1f}
.bar .g{background:#1d4d31}.bar .w{background:#fbbf24}.bar .r{background:#f87171}
.axis{display:flex;justify-content:space-between;font-size:11.5px;color:#8b8b94;margin-top:4px}
.axis .up{color:#d0d0d6}
.none{color:#8b8b94}
footer{font-size:12px;color:#8b8b94;margin-top:22px}`

// statusCSP lets the page use its one style and nothing else: no
// scripts, requests, frames or forms.
var statusCSP = func() string {
	sum := sha256.Sum256([]byte(statusCSS))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

// statusData is what the template shows.
type statusData struct {
	Title, Headline, Tone, Updated string
	CSS                            template.CSS
	Groups                         []statusGroup
}

type statusGroup struct {
	Name       string
	Components []statusComponent
}

type statusComponent struct {
	Name, Description, Label, Tone, Uptime string
	// Bar is the 90 days' markup (statusBar), written out here rather
	// than by the template, which would take most of a render on it.
	Bar template.HTML
}

// statusDay is a day of a bar: its colour class (g fine, w some downtime,
// r more, n no probes) and what hovering it says.
type statusDay struct{ Class, Title string }

// statusLabels are the words for each status.
var statusLabels = map[apitypes.ComponentStatus]string{
	apitypes.StatusOperational: "Operational", apitypes.StatusIdle: "Idle", apitypes.StatusDegraded: "Degraded",
	apitypes.StatusPartialOutage: "Partial outage", apitypes.StatusMajorOutage: "Major outage",
}

// renderStatusPage renders a page's HTML.
func renderStatusPage(p apitypes.PublicStatusPage) ([]byte, error) {
	d := statusData{Title: p.Title, Tone: string(p.Status), Headline: statusHeadline(p),
		Updated: p.GeneratedAt.UTC().Format("15:04 MST"), CSS: template.CSS(statusCSS)}
	for _, g := range p.Groups {
		sg := statusGroup{Name: g.Name}
		for _, c := range g.Components {
			sc := statusComponent{Name: c.Name, Description: c.Description, Label: statusLabels[c.Status], Tone: string(c.Status),
				Bar: statusBar(c.Days, p.ShowUptime)}
			if p.ShowUptime && c.Uptime != nil {
				sc.Uptime = statusPercent(*c.Uptime)
			}
			sg.Components = append(sg.Components, sc)
		}
		d.Groups = append(d.Groups, sg)
	}
	var b bytes.Buffer
	if err := statusPageTmpl.Execute(&b, d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// statusHeadline is the line at the top: all is well, or the worst
// status and the components that have it.
func statusHeadline(p apitypes.PublicStatusPage) string {
	if p.Status == apitypes.StatusOperational || p.Status == apitypes.StatusIdle {
		return "All systems operational"
	}
	var names []string
	for _, g := range p.Groups {
		for _, c := range g.Components {
			if c.Status == p.Status {
				names = append(names, c.Name)
			}
		}
	}
	return statusLabels[p.Status] + ": " + strings.Join(names, ", ")
}

// statusBar is a component's days as the bar's cells. Only Dock's own
// words go in (dates, percentages, fixed phrases), nothing a person
// typed, so it needs no escaping.
func statusBar(days []apitypes.StatusDay, numbers bool) template.HTML {
	var b strings.Builder
	b.Grow(len(days) * 40)
	for _, d := range days {
		day := statusDayOf(d, numbers)
		b.WriteString(`<i class="`)
		b.WriteString(day.Class)
		b.WriteString(`" title="`)
		b.WriteString(day.Title)
		b.WriteString(`"></i>`)
	}
	return template.HTML(b.String())
}

// statusMonths are the months' short names, for dates on the bars.
var statusMonths = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// statusDate is "2026-11-09" as "9 Nov", without parsing a time per day.
func statusDate(d string) string {
	if len(d) != 10 {
		return d
	}
	m, err := strconv.Atoi(d[5:7])
	if err != nil || m < 1 || m > 12 {
		return d
	}
	return strings.TrimPrefix(d[8:], "0") + " " + statusMonths[m-1]
}

// statusDayOf is a day's colour and hover text. A page that hides
// percentages says only whether there was downtime.
func statusDayOf(d apitypes.StatusDay, numbers bool) statusDay {
	date := statusDate(d.Date)
	if d.Uptime == nil {
		return statusDay{Class: "n", Title: date + ": no data"}
	}
	u := *d.Uptime
	out := statusDay{Class: "r"}
	switch {
	case u >= 100:
		out.Class = "g"
	case u >= 99:
		out.Class = "w"
	}
	switch {
	case numbers:
		out.Title = date + ": " + statusPercent(u)
	case out.Class == "g":
		out.Title = date + ": no downtime"
	default:
		out.Title = date + ": downtime"
	}
	return out
}

// statusPercent shows 100% only for a perfect record and otherwise at
// most 99.99%, so a single failed probe never rounds away.
func statusPercent(p float64) string {
	if p >= 100 {
		return "100%"
	}
	return strconv.FormatFloat(min(p, 99.99), 'f', 2, 64) + "%"
}

// statusJSONPage is a page's status.json: for other dashboards and
// agents. Field names only ever get added.
type statusJSONPage struct {
	Page       string                `json:"page"`
	Status     string                `json:"status"`
	UpdatedAt  time.Time             `json:"updated_at"`
	Components []statusJSONComponent `json:"components"`
}

type statusJSONComponent struct {
	Name   string   `json:"name"`
	Group  string   `json:"group,omitempty"`
	Status string   `json:"status"`
	Uptime *float64 `json:"uptime_90d,omitempty"`
}

// renderStatusJSON renders a page's status.json.
func renderStatusJSON(p apitypes.PublicStatusPage) ([]byte, error) {
	out := statusJSONPage{Page: p.Title, Status: string(p.Status), UpdatedAt: p.GeneratedAt.UTC(), Components: []statusJSONComponent{}}
	for _, g := range p.Groups {
		for _, c := range g.Components {
			out.Components = append(out.Components, statusJSONComponent{Name: c.Name, Group: g.Name, Status: string(c.Status), Uptime: c.Uptime})
		}
	}
	return json.Marshal(out)
}
