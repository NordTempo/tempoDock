package analytics_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/analytics/fakestore"
)

// BenchmarkClientDashboard is the control plane's side of a site's
// dashboard over loopback: one request (store=current), or the twelve a
// store from before DashboardPath takes (store=old). The fake store's
// answers are small, so this is mostly the requests' cost.
func BenchmarkClientDashboard(b *testing.B) {
	const token = "0123456789abcdef0123456789abcdef"
	fs := fakestore.New(token)
	srv := httptest.NewServer(fs.Handler())
	defer srv.Close()
	c := &analytics.Client{BaseURL: srv.URL, Token: token}
	ctx := context.Background()
	t := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	events := make([]analytics.Event, 200)
	for i := range events {
		events[i] = analytics.Event{Time: t.Add(time.Duration(i) * time.Minute).UnixMilli(), Site: 1, Kind: analytics.KindPageview,
			Path: []string{"/", "/a", "/b", "/c"}[i%4], Visitor: uint64(i%50 + 1), Browser: "Firefox", Country: "NO", Channel: "direct"}
	}
	if _, err := c.Write(ctx, "server-1", 1, events); err != nil {
		b.Fatal(err)
	}
	q := analytics.DashboardQuery{Query: analytics.Query{Site: 1, From: t.UnixMilli(), To: t.Add(24 * time.Hour).UnixMilli()},
		Bucket: analytics.BucketHour, Limit: 10, Dimensions: []analytics.Dimension{analytics.DimPage, analytics.DimEntryPage, analytics.DimReferrer,
			analytics.DimChannel, analytics.DimCountry, analytics.DimBrowser, analytics.DimDevice, analytics.DimEvent, analytics.DimService}}
	for _, old := range []bool{false, true} {
		name := "store=current"
		if old {
			name = "store=old"
		}
		b.Run(name, func(b *testing.B) {
			fs.SetOld(old)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := c.Dashboard(ctx, q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
