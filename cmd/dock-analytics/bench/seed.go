// Package bench seeds a store with realistic synthetic traffic and times
// the dashboards' queries on it: the engine spike of ADR 0037, and the
// query budget `make perf` checks.
package bench

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Writer is where seeded events go: the DuckDB store, or the SQLite
// comparison.
type Writer interface {
	Write(ctx context.Context, source string, position uint64, events []analytics.Event) (int, error)
}

// SeedOptions shape the synthetic traffic.
type SeedOptions struct {
	// Events is about how many events to write.
	Events int
	// Days is how far back the traffic goes, ending at End.
	Days int
	End  time.Time
	// Seed makes runs repeatable.
	Seed uint64
	// Progress, when set, is called after every day.
	Progress func(day, days, written int)
}

// MainSite is the site most traffic goes to (90%); sites 2 to 5 share
// the rest.
const MainSite = 1

// Seed writes about opts.Events events: sessions of one to a dozen page
// views spread over opts.Days days with a weekly rhythm, Zipf-distributed
// pages, referrers, countries and cities, a realistic browser and device
// mix, UTM campaigns on some sessions, custom events and outbound clicks,
// and two services with a release every few days. Events are written in
// time order, in batches of 10,000, as the forwarder would.
func Seed(ctx context.Context, w Writer, opts SeedOptions) (int, error) {
	r := rand.New(rand.NewPCG(opts.Seed, opts.Seed^0x5eed))
	g := newGen(r)
	perDay := float64(opts.Events) / float64(opts.Days)
	start := opts.End.Truncate(24*time.Hour).AddDate(0, 0, -opts.Days+1)
	var batch []analytics.Event
	var pos uint64
	written := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		pos++
		n, err := w.Write(ctx, "seed", pos, batch)
		written += n
		batch = batch[:0]
		return err
	}
	for day := range opts.Days {
		d := start.AddDate(0, 0, day)
		// Weekdays busier than weekends, and slow growth over the period.
		f := 1.0
		if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
			f = 0.7
		}
		f *= 0.8 + 0.4*float64(day)/float64(opts.Days)
		target := int(perDay * f * 1.1) // the weekend dip averages out about here
		var dayEvents []analytics.Event
		// The target counts page views and custom events; errors and
		// Web Vitals come on top, as they would.
		for counted := 0; counted < target; {
			n := len(dayEvents)
			dayEvents = g.session(dayEvents, d, day)
			for _, e := range dayEvents[n:] {
				if e.Kind <= analytics.KindOutbound {
					counted++
				}
			}
		}
		slices.SortFunc(dayEvents, func(a, b analytics.Event) int { return int(a.Time - b.Time) })
		for _, e := range dayEvents {
			if e.Time >= opts.End.UnixMilli() {
				continue
			}
			batch = append(batch, e)
			if len(batch) == 10_000 {
				if err := flush(); err != nil {
					return written, err
				}
			}
		}
		if opts.Progress != nil {
			opts.Progress(day+1, opts.Days, written+len(batch))
		}
	}
	return written, flush()
}

// gen draws sessions.
type gen struct {
	r                                *rand.Rand
	paths, referrers, cities, events *rand.Zipf
	pathNames                        []string
	refNames                         []string
	countries                        []string
	countryZipf                      *rand.Zipf
	errors                           *rand.Zipf
}

func newGen(r *rand.Rand) *gen {
	g := &gen{r: r}
	g.pathNames = []string{"/", "/pricing", "/docs", "/blog", "/about", "/signup", "/login", "/features", "/contact", "/changelog"}
	for i := len(g.pathNames); i < 2000; i++ {
		switch i % 3 {
		case 0:
			g.pathNames = append(g.pathNames, "/blog/post-"+strconv.Itoa(i))
		case 1:
			g.pathNames = append(g.pathNames, "/docs/page-"+strconv.Itoa(i))
		default:
			g.pathNames = append(g.pathNames, "/product/"+strconv.Itoa(i))
		}
	}
	g.refNames = []string{"google.com", "bing.com", "duckduckgo.com", "news.ycombinator.com", "reddit.com", "twitter.com",
		"linkedin.com", "facebook.com", "github.com", "youtube.com"}
	for i := len(g.refNames); i < 300; i++ {
		g.refNames = append(g.refNames, "site"+strconv.Itoa(i)+".example")
	}
	g.countries = []string{"US", "DE", "GB", "NO", "FR", "IN", "SE", "NL", "CA", "BR", "ES", "IT", "PL", "JP", "AU", "DK", "FI", "CH", "AT", "BE"}
	for i := len(g.countries); i < 120; i++ {
		g.countries = append(g.countries, string(rune('A'+i/26%26))+string(rune('A'+i%26)))
	}
	g.paths = rand.NewZipf(r, 1.2, 4, uint64(len(g.pathNames)-1))
	g.referrers = rand.NewZipf(r, 1.3, 2, uint64(len(g.refNames)-1))
	g.countryZipf = rand.NewZipf(r, 1.1, 3, uint64(len(g.countries)-1))
	g.cities = rand.NewZipf(r, 1.4, 2, 40)
	g.events = rand.NewZipf(r, 1.5, 1, 19)
	g.errors = rand.NewZipf(r, 1.3, 1, 59)
	return g
}

var (
	browsers = []struct {
		name  string
		share float64
	}{{"Chrome", 0.6}, {"Safari", 0.2}, {"Firefox", 0.08}, {"Edge", 0.07}, {"Samsung Internet", 0.03}, {"Opera", 0.02}}
	oses      = []string{"Windows", "macOS", "iOS", "Android", "Linux"}
	screens   = [][2]uint16{{1920, 1080}, {1440, 900}, {1536, 864}, {2560, 1440}, {1366, 768}, {390, 844}, {393, 873}, {414, 896}, {375, 812}, {412, 915}, {820, 1180}, {768, 1024}}
	languages = []string{"en-US", "en-GB", "de-DE", "nb-NO", "fr-FR", "es-ES", "sv-SE", "nl-NL", "pl-PL", "ja-JP", "pt-BR", "it-IT"}
	sources   = []string{"newsletter", "twitter", "linkedin", "google", "producthunt", "partner"}
	mediums   = []string{"email", "social", "cpc", "referral", "banner"}
)

// session appends one session's events on day d.
func (g *gen) session(out []analytics.Event, d time.Time, day int) []analytics.Event {
	r := g.r
	site := int64(MainSite)
	if r.Float64() < 0.1 {
		site = 2 + r.Int64N(4)
	}
	// About half the day's visitors come back for a second session.
	visitor := r.Uint64()
	// Traffic peaks in the afternoon (UTC). Sessions end the day they
	// start, as real ones do: the visitor ID's salt changes at midnight.
	sec := int64(math.Mod(math.Abs(r.NormFloat64()*4*3600+15*3600), 86400-3600))
	t := d.UnixMilli() + sec*1000 + r.Int64N(1000)

	host := "example.com"
	if site != MainSite {
		host = "site" + strconv.FormatInt(site, 10) + ".example.com"
	} else if r.Float64() < 0.05 {
		host = "www.example.com"
	}
	service := site * 10
	if site == MainSite && day > 200 {
		service = 11 // the site moved to a new service
	}
	release := service*1000 + int64(day/3)

	e := analytics.Event{Site: site, Service: service, Release: release, Hostname: host, Visitor: visitor}
	cc := g.countries[g.countryZipf.Uint64()]
	e.Language = languages[r.IntN(len(languages))]
	x := r.Float64()
	for _, b := range browsers {
		if x -= b.share; x <= 0 {
			e.Browser = b.name
			break
		}
	}
	if e.Browser == "" {
		e.Browser = "Chrome"
	}
	e.BrowserVersion = strconv.Itoa(100 + r.IntN(30))
	e.OS = oses[r.IntN(len(oses))]
	e.OSVersion = strconv.Itoa(10 + r.IntN(8))
	s := screens[r.IntN(len(screens))]
	e.ScreenW, e.ScreenH = s[0], s[1]
	switch e.OS {
	case "iOS", "Android":
		e.Device = "mobile"
		if s[0] >= 768 {
			e.Device = "tablet"
		}
	default:
		e.Device = "desktop"
	}
	if r.Float64() < 0.6 {
		e.Referrer = g.refNames[g.referrers.Uint64()]
		switch e.Referrer {
		case "google.com", "bing.com", "duckduckgo.com":
			e.Channel = "search"
		case "twitter.com", "linkedin.com", "facebook.com", "reddit.com", "youtube.com", "news.ycombinator.com":
			e.Channel = "social"
		default:
			e.Channel = "referral"
		}
	} else {
		e.Channel = "direct"
	}
	if r.Float64() < 0.08 {
		e.UTMSource = sources[r.IntN(len(sources))]
		e.UTMMedium = mediums[r.IntN(len(mediums))]
		e.UTMCampaign = "campaign-" + strconv.Itoa(r.IntN(30))
		if e.UTMMedium == "email" {
			e.Channel = "email"
		}
	}
	// The country goes in the IP prefix's place: the bench's locator
	// reads it back (seedLocator).
	e.IPPrefix = cc + "/" + strconv.FormatUint(g.cities.Uint64(), 10)

	pages := 1
	for r.Float64() < 0.55 && pages < 12 {
		pages++
	}
	for i := range pages {
		pv := e
		pv.Kind = analytics.KindPageview
		pv.Path = g.pathNames[g.paths.Uint64()]
		if i > 0 {
			pv.Referrer = ""
		}
		pv.Time = t
		out = append(out, pv)
		// Most page loads report their vitals; mobile ones are slower.
		if r.Float64() < 0.8 {
			slow := 1.0
			if pv.Device != "desktop" {
				slow = 1.8
			}
			vt := pv
			vt.Kind, vt.Time = analytics.KindVitals, t+3000+r.Int64N(60_000)
			vt.Vitals = &analytics.Vitals{
				LCP: math.Round(slow * 900 * math.Exp(r.NormFloat64()*0.5)), INP: -1,
				CLS: math.Round(math.Abs(r.NormFloat64())*0.06*10000) / 10000,
				FCP: math.Round(slow * 600 * math.Exp(r.NormFloat64()*0.4)), TTFB: math.Round(150 * math.Exp(r.NormFloat64()*0.6)),
			}
			if r.Float64() < 0.6 {
				vt.Vitals.INP = math.Round(slow * 80 * math.Exp(r.NormFloat64()*0.7))
			}
			out = append(out, vt)
		}
		if r.Float64() < 0.01 {
			n := g.errors.Uint64()
			er := pv
			er.Kind, er.Time = analytics.KindError, t+500+r.Int64N(30_000)
			er.Name = "Cannot read properties of undefined (reading 'f" + strconv.FormatUint(n, 10) + "')"
			frame := "render" + strconv.FormatUint(n%7, 10) + " (/assets/index.js)"
			er.Error = &analytics.ErrorInfo{Type: "TypeError", Frame: frame, Group: 1000 + n,
				Stack: "TypeError: " + er.Name + "\n    at " + frame + "\n    at main (/assets/index.js)"}
			out = append(out, er)
		}
		if r.Float64() < 0.07 {
			ev := pv
			ev.Kind = analytics.KindEvent
			ev.Name = "event-" + strconv.FormatUint(g.events.Uint64(), 10)
			if ev.Name == "event-0" {
				ev.Name = "signup"
				ev.Props = `{"plan":"pro"}`
			}
			ev.Time = t + 1000 + r.Int64N(20_000)
			out = append(out, ev)
		}
		if r.Float64() < 0.03 {
			ev := pv
			ev.Kind = analytics.KindOutbound
			ev.Name = "https://" + g.refNames[g.referrers.Uint64()] + "/"
			ev.Time = t + 2000 + r.Int64N(20_000)
			out = append(out, ev)
		}
		t += 5_000 + r.Int64N(120_000)
	}
	return out
}

// SeedLocator turns the seed's stand-in prefixes ("NO/3") into a country,
// region and city, as the GeoIP lookup would.
func SeedLocator(prefix string) (country, region, city string) {
	for i := range len(prefix) {
		if prefix[i] == '/' {
			cc, n := prefix[:i], prefix[i+1:]
			return cc, cc + "-R" + fmt.Sprint(len(n)%3), cc + " City " + n
		}
	}
	return "", "", ""
}
