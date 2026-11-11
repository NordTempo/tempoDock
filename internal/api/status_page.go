package api

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"html/template"
	"slices"
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

// statusPageTmpl holds the pages' templates: page (/), one
// (/incidents/<id>) and history (/history).
var statusPageTmpl = template.Must(template.New("status").Funcs(template.FuncMap{
	"css": func() template.CSS { return template.CSS(statusCSS) },
}).Parse(statusPageSource))

// statusCSS is the page's style, allowed by its hash.
const statusCSS = `*{box-sizing:border-box}
body{margin:0;background:#000;color:#fafafa;font:14px/1.45 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Inter,Roboto,sans-serif;font-variant-numeric:tabular-nums}
main{max-width:720px;margin:0 auto;padding:28px 20px 32px}
a{color:inherit;text-decoration:none}
a:hover{text-decoration:underline}
header{display:flex;align-items:baseline;gap:12px}
h1{font-size:15px;font-weight:600;margin:0;flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
header a{font-size:12px;color:#8b8b94}
h1 a{color:inherit;font-size:15px}
.state{display:flex;align-items:baseline;flex-wrap:wrap;gap:0 12px;font-size:20px;font-weight:600;margin:18px 0 6px}
.upd{font-size:13px;font-weight:400;color:#8b8b94}
.dot{display:inline-block;width:10px;height:10px;border-radius:999px;align-self:center}
.dot.operational{background:#4ade80}.dot.maintenance{background:#60a5fa}.dot.degraded{background:#fbbf24}.dot.partial_outage{background:#fb923c}.dot.major_outage{background:#f87171}
h2{font-size:15px;font-weight:600;margin:24px 0 2px}
section{padding:9px 0 10px;border-bottom:1px solid #161618}
.row{display:flex;justify-content:space-between;gap:12px;margin-bottom:6px}
.name{min-width:0}
.desc{color:#8b8b94;font-size:13px}
.operational{color:#4ade80}.idle{color:#8b8b94}.maintenance{color:#60a5fa}.degraded{color:#fbbf24}.partial_outage{color:#fb923c}.major_outage{color:#f87171}
.bar{display:flex;gap:2px;height:28px}
.bar i{flex:1;min-width:1px;border-radius:1px;background:#1c1c1f}
.bar .g{background:#1d4d31}.bar .w{background:#fbbf24}.bar .o{background:#fb923c}.bar .b{background:#2563eb}.bar .r{background:#f87171}
.inc{border-left:2px solid #3f3f46;padding:2px 0 2px 14px;margin:0 0 16px}
.inc.b-degraded{border-color:#fbbf24}.inc.b-partial_outage{border-color:#fb923c}.inc.b-major_outage{border-color:#f87171}.inc.b-maintenance{border-color:#60a5fa}
.inc-t{font-weight:600;margin-bottom:4px}
.inc-t span{font-weight:400;margin-left:8px}
.u{margin:6px 0;font-size:13.5px}
.u p{margin:2px 0 0}
.u a{text-decoration:underline}
time{color:#8b8b94;font-family:ui-monospace,Menlo,monospace;font-size:12.5px;margin-right:10px}
.past{display:grid;grid-template-columns:64px 1fr auto;gap:12px;font-size:13.5px;padding:6px 0;border-bottom:1px solid #161618}
.more{font-size:13px;color:#8b8b94}
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

// statusData is what the page template shows.
type statusData struct {
	Title, Headline, Tone, Updated string
	Open, Maintenance              []statusIncidentView
	Groups                         []statusGroup
	Past                           []statusPast
}

// statusIncidentView is an incident as the pages show it: Tone is its
// worst impact's border, Link whether its title links to its own page.
type statusIncidentView struct {
	// Window is a maintenance window's time ("13 Nov 02:00 to 02:30 UTC").
	ID, Title, State, Tone, Affects, Window string
	Link                                    bool
	Updates                                 []statusUpdateView
}

// statusUpdateView is a posted update; Body is its words as paragraphs
// with links (statusText).
type statusUpdateView struct {
	When, State string
	Edited      bool
	Body        template.HTML
}

// statusPast is a line of the page's past incidents: one incident, or a
// day without any (no ID).
type statusPast struct{ Date, ID, Title, State string }

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
	apitypes.StatusMaintenance: "Maintenance",
}

// incidentStateLabels are the words for an incident's states.
var incidentStateLabels = map[apitypes.IncidentState]string{
	apitypes.IncidentInvestigating: "Investigating", apitypes.IncidentIdentified: "Identified",
	apitypes.IncidentMonitoring: "Monitoring", apitypes.IncidentResolved: "Resolved",
	apitypes.MaintenanceScheduled: "Scheduled", apitypes.MaintenanceInProgress: "In progress",
	apitypes.MaintenanceCompleted: "Completed", apitypes.MaintenanceCancelled: "Cancelled",
}

// renderStatusPage renders a page's HTML.
func renderStatusPage(p apitypes.PublicStatusPage) ([]byte, error) {
	now := p.GeneratedAt.UTC()
	d := statusData{Title: p.Title, Tone: string(p.Status), Headline: statusHeadline(p), Updated: now.Format("15:04 MST")}
	for _, inc := range p.Open {
		d.Open = append(d.Open, incidentView(inc, now, true))
	}
	for _, w := range p.Maintenance {
		d.Maintenance = append(d.Maintenance, incidentView(w, now, true))
	}
	d.Past = statusPastDays(p, now)
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
	return execStatus("page", d)
}

// execStatus runs one of the pages' templates.
func execStatus(name string, d any) ([]byte, error) {
	var b bytes.Buffer
	if err := statusPageTmpl.ExecuteTemplate(&b, name, d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// incidentView is an incident for the templates, its updates newest
// first with times relative to now's day.
func incidentView(inc apitypes.PublicIncident, now time.Time, link bool) statusIncidentView {
	v := statusIncidentView{ID: inc.ID, Title: inc.Title, State: incidentStateLabels[inc.State], Link: link, Tone: "b-operational"}
	if inc.Kind == apitypes.KindMaintenance {
		v.Tone, v.Window = "b-maintenance", statusWindow(inc)
		names := make([]string, len(inc.Components))
		for i, im := range inc.Components {
			names[i] = im.Component
		}
		if len(names) > 0 {
			v.Affects = "Affects " + strings.Join(names, ", ")
		}
	} else if inc.ResolvedAt == nil {
		worst := apitypes.ComponentStatus("")
		var names []string
		for _, im := range inc.Components {
			if statusRank(im.Impact) > statusRank(worst) {
				worst = im.Impact
			}
			names = append(names, im.Component)
		}
		if worst != "" {
			v.Tone = "b-" + string(worst)
		}
		if len(names) > 0 {
			v.Affects = "Affects " + strings.Join(names, ", ")
		}
	}
	for _, u := range inc.Updates {
		v.Updates = append(v.Updates, statusUpdateView{When: statusWhen(u.At, now), State: incidentStateLabels[u.State], Edited: u.Edited, Body: statusText(u.Message)})
	}
	return v
}

// statusWindow is a maintenance window's time as "Thu 13 Nov 02:00 to
// 02:30 UTC": its planned end, or its real one once over.
func statusWindow(w apitypes.PublicIncident) string {
	start := w.StartedAt.UTC()
	var end time.Time
	switch {
	case w.ResolvedAt != nil && w.State == apitypes.MaintenanceCompleted:
		end = w.ResolvedAt.UTC()
	case w.EndsAt != nil:
		end = w.EndsAt.UTC()
	default:
		return start.Format("Mon 2 Jan 15:04 MST")
	}
	layout := "15:04 MST"
	if end.YearDay() != start.YearDay() || end.Year() != start.Year() {
		layout = "Mon 2 Jan 15:04 MST"
	}
	return start.Format("Mon 2 Jan 15:04") + " to " + end.Format(layout)
}

// statusRank orders impacts from none to a major outage.
func statusRank(s apitypes.ComponentStatus) int {
	switch s {
	case apitypes.StatusDegraded:
		return 1
	case apitypes.StatusPartialOutage:
		return 2
	case apitypes.StatusMajorOutage:
		return 3
	}
	return 0
}

// statusWhen is a time as "14:02" on now's day, "9 Nov 14:02" before.
func statusWhen(t, now time.Time) string {
	t = t.UTC()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

// statusPastDays are the page's last StatusRecentDays days, newest
// first: each day's incidents and windows that began (by the day they
// started, or the window's first day for older ones), or a line saying it
// had none. Open incidents and windows in progress count too, so a day
// with one never reads "No incidents"; scheduled windows haven't begun.
func statusPastDays(p apitypes.PublicStatusPage, now time.Time) []statusPast {
	today := now.Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(apitypes.StatusRecentDays - 1))
	byDay := map[string][]apitypes.PublicIncident{}
	begun := slices.Concat(p.Open, p.Recent)
	for _, w := range p.Maintenance {
		if w.State == apitypes.MaintenanceInProgress {
			begun = append(begun, w)
		}
	}
	for _, inc := range begun {
		day := inc.StartedAt.UTC().Truncate(24 * time.Hour)
		if day.Before(first) {
			day = first
		}
		k := day.Format(time.DateOnly)
		byDay[k] = append(byDay[k], inc)
	}
	var out []statusPast
	for d := today; !d.Before(first); d = d.AddDate(0, 0, -1) {
		date := d.Format("2 Jan")
		incs := byDay[d.Format(time.DateOnly)]
		if len(incs) == 0 {
			out = append(out, statusPast{Date: date})
		}
		for _, inc := range incs {
			out = append(out, statusPast{Date: date, ID: inc.ID, Title: inc.Title, State: incidentStateLabels[inc.State]})
		}
	}
	return out
}

// statusText is an update's words as HTML: paragraphs at blank lines,
// line breaks kept, https links made links, everything else escaped.
func statusText(s string) template.HTML {
	var b strings.Builder
	for _, para := range strings.Split(strings.TrimSpace(s), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		b.WriteString("<p>")
		for i, line := range strings.Split(para, "\n") {
			if i > 0 {
				b.WriteString("<br>")
			}
			writeLinked(&b, line)
		}
		b.WriteString("</p>")
	}
	return template.HTML(b.String())
}

// writeLinked writes a line escaped, with its https links as links.
// Punctuation that ends a sentence isn't part of a link.
func writeLinked(b *strings.Builder, line string) {
	for {
		i := strings.Index(line, "https://")
		if i < 0 {
			b.WriteString(template.HTMLEscapeString(line))
			return
		}
		b.WriteString(template.HTMLEscapeString(line[:i]))
		end := strings.IndexFunc(line[i:], func(r rune) bool { return r == ' ' || r == '\t' || r == '<' || r == '>' || r == '"' })
		if end < 0 {
			end = len(line) - i
		}
		u := strings.TrimRight(line[i:i+end], ".,;:!?)'")
		if len(u) <= len("https://") {
			b.WriteString(template.HTMLEscapeString(u))
		} else {
			esc := template.HTMLEscapeString(u)
			b.WriteString(`<a href="` + esc + `" rel="nofollow noopener">` + esc + `</a>`)
		}
		line = line[i+len(u):]
	}
}

// statusOneData is what an incident's own page shows.
type statusOneData struct {
	Title, Span string
	Incident    statusIncidentView
}

// renderIncidentPage renders an incident's own page on the page titled
// title.
func renderIncidentPage(title string, inc apitypes.PublicIncident, now time.Time) ([]byte, error) {
	if inc.Kind == apitypes.KindMaintenance {
		return execStatus("one", statusOneData{Title: title, Span: statusWindow(inc), Incident: incidentView(inc, now.UTC(), false)})
	}
	start := inc.StartedAt.UTC()
	span := "since " + start.Format("2 Jan 15:04 MST")
	if inc.ResolvedAt != nil {
		end := inc.ResolvedAt.UTC()
		layout := "15:04 MST"
		if end.YearDay() != start.YearDay() || end.Year() != start.Year() {
			layout = "2 Jan 15:04 MST"
		}
		span = start.Format("2 Jan 15:04") + " to " + end.Format(layout)
	}
	return execStatus("one", statusOneData{Title: title, Span: span, Incident: incidentView(inc, now.UTC(), false)})
}

// statusHistoryData is what /history shows: incidents by month.
type statusHistoryData struct {
	Title  string
	Months []statusMonth
	More   bool
}

type statusMonth struct {
	Name      string
	Incidents []statusPast
}

// renderHistoryPage renders a page's /history.
func renderHistoryPage(h apitypes.PublicStatusHistory) ([]byte, error) {
	d := statusHistoryData{Title: h.Title, More: h.More}
	for _, inc := range h.Incidents {
		t := inc.StartedAt.UTC()
		name := t.Format("January 2006")
		if n := len(d.Months); n == 0 || d.Months[n-1].Name != name {
			d.Months = append(d.Months, statusMonth{Name: name})
		}
		m := &d.Months[len(d.Months)-1]
		m.Incidents = append(m.Incidents, statusPast{Date: t.Format("2 Jan"), ID: inc.ID, Title: inc.Title, State: incidentStateLabels[inc.State]})
	}
	return execStatus("history", d)
}

// statusHeadline is the line at the top: all is well, or the worst
// status and the components that have it.
func statusHeadline(p apitypes.PublicStatusPage) string {
	if p.Status == apitypes.StatusOperational || p.Status == apitypes.StatusIdle {
		return "All systems operational"
	}
	label := statusLabels[p.Status]
	if p.Status == apitypes.StatusMaintenance {
		label = "Under maintenance"
	}
	var names []string
	for _, g := range p.Groups {
		for _, c := range g.Components {
			if c.Status == p.Status {
				names = append(names, c.Name)
			}
		}
	}
	return label + ": " + strings.Join(names, ", ")
}

// statusBar is a component's days as the bar's cells. Dock's own words
// (dates, percentages, fixed phrases) go in as they are; an incident's
// title, which a person typed, escaped.
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

// dayClasses order a day's colours from no data to red; an incident's
// impact makes a day at least its colour.
var (
	dayClasses  = map[string]int{"n": 0, "g": 1, "w": 2, "o": 3, "r": 4}
	impactClass = map[apitypes.ComponentStatus]string{apitypes.StatusDegraded: "w", apitypes.StatusPartialOutage: "o", apitypes.StatusMajorOutage: "r"}
)

// statusDayOf is a day's colour and hover text, escaped for an
// attribute. A page that hides percentages says only whether there was
// downtime.
func statusDayOf(d apitypes.StatusDay, numbers bool) statusDay {
	date := statusDate(d.Date)
	out := statusDay{Class: "n"}
	if d.Uptime == nil {
		out.Title = date + ": no data"
	} else {
		u := *d.Uptime
		out.Class = "r"
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
	}
	if d.Incident != "" {
		if c := impactClass[d.Impact]; dayClasses[c] > dayClasses[out.Class] {
			out.Class = c
		}
		out.Title += ", " + template.HTMLEscapeString(d.Incident)
	}
	if d.Maintenance {
		// Planned work is blue unless something worse happened.
		if out.Class == "n" || out.Class == "g" {
			out.Class = "b"
		}
		out.Title += ", maintenance"
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
	Incidents  []statusJSONIncident  `json:"incidents"`
	// Maintenance are the windows scheduled or in progress, soonest
	// first.
	Maintenance []statusJSONIncident `json:"maintenance"`
}

// statusJSONIncident is an open incident in status.json, updates newest
// first.
type statusJSONIncident struct {
	ID         string                  `json:"id"`
	URL        string                  `json:"url"`
	Title      string                  `json:"title"`
	State      apitypes.IncidentState  `json:"state"`
	StartedAt  time.Time               `json:"started_at"`
	EndsAt     *time.Time              `json:"ends_at,omitempty"`
	Components []apitypes.StatusImpact `json:"components"`
	Updates    []statusJSONUpdate      `json:"updates"`
}

type statusJSONUpdate struct {
	State   apitypes.IncidentState `json:"state"`
	Message string                 `json:"message"`
	At      time.Time              `json:"at"`
	Edited  bool                   `json:"edited,omitempty"`
}

type statusJSONComponent struct {
	Name   string   `json:"name"`
	Group  string   `json:"group,omitempty"`
	Status string   `json:"status"`
	Uptime *float64 `json:"uptime_90d,omitempty"`
}

// jsonIncident is an incident or window in status.json.
func jsonIncident(inc apitypes.PublicIncident) statusJSONIncident {
	ji := statusJSONIncident{ID: inc.ID, URL: "/incidents/" + inc.ID, Title: inc.Title, State: inc.State, StartedAt: inc.StartedAt.UTC(),
		Components: inc.Components, Updates: make([]statusJSONUpdate, len(inc.Updates))}
	if inc.EndsAt != nil {
		end := inc.EndsAt.UTC()
		ji.EndsAt = &end
	}
	for i, u := range inc.Updates {
		ji.Updates[i] = statusJSONUpdate{State: u.State, Message: u.Message, At: u.At.UTC(), Edited: u.Edited}
	}
	return ji
}

// renderStatusJSON renders a page's status.json.
func renderStatusJSON(p apitypes.PublicStatusPage) ([]byte, error) {
	out := statusJSONPage{Page: p.Title, Status: string(p.Status), UpdatedAt: p.GeneratedAt.UTC(), Components: []statusJSONComponent{},
		Incidents: []statusJSONIncident{}, Maintenance: []statusJSONIncident{}}
	for _, inc := range p.Open {
		out.Incidents = append(out.Incidents, jsonIncident(inc))
	}
	for _, w := range p.Maintenance {
		out.Maintenance = append(out.Maintenance, jsonIncident(w))
	}
	for _, g := range p.Groups {
		for _, c := range g.Components {
			out.Components = append(out.Components, statusJSONComponent{Name: c.Name, Group: g.Name, Status: string(c.Status), Uptime: c.Uptime})
		}
	}
	return json.Marshal(out)
}

// atomFeed is a page's Atom feed (RFC 4287): one entry per posted
// update, newest first.
type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Author  atomAuthor  `xml:"author"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Href string `xml:"href,attr"`
}

type atomEntry struct {
	Title     string      `xml:"title"`
	ID        string      `xml:"id"`
	Link      atomLink    `xml:"link"`
	Published string      `xml:"published"`
	Updated   string      `xml:"updated"`
	Content   atomContent `xml:"content"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// renderStatusFeed renders a page's feed; base is the page's address
// (scheme and host), for the absolute links Atom wants.
func renderStatusFeed(f apitypes.PublicStatusFeed, base string) ([]byte, error) {
	out := atomFeed{Title: f.Title + " status", ID: base + "/", Updated: f.Updated.UTC().Format(time.RFC3339), Author: atomAuthor{Name: f.Title},
		Links: []atomLink{{Rel: "alternate", Href: base + "/"}, {Rel: "self", Href: base + "/feed.atom"}}}
	for _, e := range f.Entries {
		link := base + "/incidents/" + e.Incident
		at := e.At.UTC().Format(time.RFC3339)
		out.Entries = append(out.Entries, atomEntry{Title: e.Title + ": " + incidentStateLabels[e.State],
			ID: link + "#" + strconv.FormatInt(e.At.Unix(), 10), Link: atomLink{Href: link}, Published: at, Updated: at,
			Content: atomContent{Type: "text", Text: e.Message}})
	}
	b, err := xml.Marshal(out)
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}
