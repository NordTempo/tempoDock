package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// visitsFor names where the shop's visits happened.
type visitsFor struct {
	Site, Service, Release int64
	Host                   string
}

// weighted is one choice of a weighted pick.
type weighted[T any] struct {
	v T
	w int
}

// pick chooses one of xs, as likely as its weight.
func pick[T any](r *rand.Rand, xs []weighted[T]) T {
	total := 0
	for _, x := range xs {
		total += x.w
	}
	n := r.IntN(total)
	for _, x := range xs {
		if n -= x.w; n < 0 {
			return x.v
		}
	}
	return xs[len(xs)-1].v
}

type referrer struct{ host, channel string }
type place struct{ country, city string }
type agent struct{ browser, os, device string }

// The shop's traffic: what visitors open, where they come from, where
// they are and what they use.
var (
	paths = []weighted[string]{
		{"/", 30}, {"/products", 18}, {"/products/:id", 22}, {"/cart", 9}, {"/checkout", 6}, {"/blog/launch", 5}, {"/pricing", 7}, {"/about", 3},
	}
	referrers = []weighted[referrer]{
		{referrer{"", "direct"}, 35}, {referrer{"google.com", "search"}, 30}, {referrer{"news.ycombinator.com", "social"}, 10},
		{referrer{"twitter.com", "social"}, 8}, {referrer{"duckduckgo.com", "search"}, 5}, {referrer{"github.com", "referral"}, 6},
		{referrer{"newsletter", "email"}, 6},
	}
	places = []weighted[place]{
		{place{"NO", "Oslo"}, 30}, {place{"SE", "Stockholm"}, 12}, {place{"DE", "Berlin"}, 15}, {place{"US", "New York"}, 20},
		{place{"GB", "London"}, 10}, {place{"NL", "Amsterdam"}, 6}, {place{"FR", "Paris"}, 7},
	}
	agents = []weighted[agent]{
		{agent{"Chrome", "Mac OS", "desktop"}, 40}, {agent{"Safari", "iOS", "mobile"}, 25}, {agent{"Firefox", "Linux", "desktop"}, 10},
		{agent{"Chrome", "Android", "mobile"}, 15}, {agent{"Edge", "Windows", "desktop"}, 10},
	}
	// frontendErrors are the shop's three recurring browser errors.
	frontendErrors = []struct {
		msg, typ, frame, path string
		group                 uint64
	}{
		{"Cannot read properties of undefined (reading 'price')", "TypeError", "CartTotal (/assets/cart.js)", "/checkout", 101},
		{"Failed to fetch", "TypeError", "loadProducts (/assets/products.js)", "/products", 102},
		{"Stripe is not defined", "ReferenceError", "pay (/assets/checkout.js)", "/checkout", 103},
	}
)

// writeVisits writes the last 24 hours of the shop's visits before now
// to path, as the ingest's JSON lines (DOCK_FAKE_SPOOL): about 4,200
// pageviews by 900 visitors, add-to-cart and purchase events, web vitals
// and a few dozen frontend errors. The same seed gives the same visits.
func writeVisits(path string, at visitsFor, now time.Time) error {
	r := rand.New(rand.NewPCG(7, 7))
	end := now.UnixMilli()
	visitors := make([]uint64, 900)
	for i := range visitors {
		visitors[i] = r.Uint64() >> 1
	}
	var out []analytics.Event
	for range 4200 {
		// Busier towards now.
		t := end - int64(math.Pow(r.Float64(), 1.3)*24*3600*1000)
		ref, pl, ag := pick(r, referrers), pick(r, places), pick(r, agents)
		e := analytics.Event{
			Time: t, Site: at.Site, Service: at.Service, Release: at.Release, Hostname: at.Host,
			Kind: analytics.KindPageview, Path: pick(r, paths), Visitor: visitors[r.IntN(len(visitors))],
			Browser: ag.browser, OS: ag.os, Device: ag.device, Language: "en",
			Country: pl.country, City: pl.city, Channel: ref.channel, Referrer: ref.host,
		}
		if ref.channel == "email" {
			e.UTMSource, e.UTMMedium, e.UTMCampaign = "newsletter", "email", "november"
		}
		out = append(out, e)
		if e.Path == "/cart" && r.Float64() < 0.5 {
			ev := e
			ev.Time, ev.Kind, ev.Name = t+2000, analytics.KindEvent, "add_to_cart"
			ev.Props = fmt.Sprintf(`{"plan":%q}`, []string{"basic", "pro"}[r.IntN(2)])
			out = append(out, ev)
		}
		if e.Path == "/checkout" && r.Float64() < 0.4 {
			ev := e
			ev.Time, ev.Kind, ev.Name = t+5000, analytics.KindEvent, "purchase"
			out = append(out, ev)
		}
		if r.Float64() < 0.15 {
			ev := e
			ev.Time, ev.Kind = t+800, analytics.KindVitals
			ev.Vitals = &analytics.Vitals{
				LCP: max(300, 1900+r.NormFloat64()*600), INP: max(20, 140+r.NormFloat64()*60), CLS: math.Abs(0.05 + r.NormFloat64()*0.04),
				FCP: max(200, 900+r.NormFloat64()*250), TTFB: max(30, 220+r.NormFloat64()*80),
			}
			out = append(out, ev)
		}
	}
	for range 46 {
		fe := frontendErrors[r.IntN(len(frontendErrors))]
		fn, file, _ := strings.Cut(strings.TrimSuffix(fe.frame, ")"), " (")
		out = append(out, analytics.Event{
			Time: end - r.Int64N(20*3600*1000), Site: at.Site, Service: at.Service, Release: at.Release, Hostname: at.Host,
			Kind: analytics.KindError, Name: fe.msg, Path: fe.path, Visitor: visitors[r.IntN(len(visitors))],
			Browser: "Chrome", OS: "Mac OS", Device: "desktop", Country: "NO",
			Error: &analytics.ErrorInfo{
				Type: fe.typ, Frame: fe.frame, Group: fe.group,
				Stack: fmt.Sprintf("%s: %s\n    at %s (%s:12:7)\n    at render (/assets/app.js:88:3)", fe.typ, fe.msg, fn, file),
			},
		})
	}
	slices.SortFunc(out, func(a, b analytics.Event) int { return int(a.Time - b.Time) })

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range out {
		if err := enc.Encode(e); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
