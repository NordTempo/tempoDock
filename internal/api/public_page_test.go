package api

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// publicSample is a dashboard with a bit of everything; message holds a
// hostile error message.
func publicSample(showErrors bool, message string) apitypes.PublicStats {
	t := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	from := t.Add(-30 * 24 * time.Hour)
	st := apitypes.PublicStats{
		Site: "shop.example.com", Range: "30d", From: from.UnixMilli(), To: t.UnixMilli(), StoreOnline: true, GeneratedAt: t,
		Overview:     apitypes.AnalyticsOverview{Visitors: 18420, Pageviews: 61207, BounceRate: 0.41, DurationMS: 168_000},
		Previous:     apitypes.AnalyticsOverview{Visitors: 16446, Pageviews: 65000},
		LiveVisitors: 23, ShowErrors: showErrors,
		Breakdowns: map[string][]apitypes.PublicRow{
			"page":     {{Value: "/", Visitors: 6120}, {Value: "/pricing", Visitors: 2140}},
			"referrer": {{Value: "", Visitors: 7210}, {Value: "google.com", Visitors: 5904}},
			"country":  {{Value: "NO", Visitors: 8120}},
		},
		Errors: []apitypes.PublicError{{Type: "TypeError", Message: message, Visitors: 389}},
	}
	for i := range 30 {
		st.Series = append(st.Series, apitypes.AnalyticsPoint{T: from.Add(time.Duration(i) * 24 * time.Hour).UnixMilli(), Visitors: int64(400 + i*10)})
	}
	return st
}

func TestRenderPublic(t *testing.T) {
	hostile := `<img src=x onerror=alert(1)>`
	b, err := renderPublic(publicSample(false, hostile))
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, want := range []string{"18,420", "12%", "−6%", "41%", "2m 48s", "23 people on the site now", "Direct", `data-cc="NO"`, `aria-current="page">30d`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "Errors") || strings.Contains(page, "TypeError") {
		t.Error("errors shown on a site that doesn't show them")
	}

	b, _ = renderPublic(publicSample(true, hostile))
	page = string(b)
	if !strings.Contains(page, "TypeError: ") || strings.Contains(page, hostile) {
		t.Error("errors not shown, or shown unescaped")
	}
	// The policy's hash is the script the page runs.
	m := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no script")
	}
	sum := sha256.Sum256([]byte(m[1]))
	if !strings.Contains(publicCSP, base64.StdEncoding.EncodeToString(sum[:])) {
		t.Errorf("the policy doesn't allow the page's script %q", m[1])
	}
	if out := os.Getenv("PUBLIC_PAGE_OUT"); out != "" {
		_ = os.WriteFile(out, b, 0o600)
	}

	b, _ = renderPublic(apitypes.PublicStats{Site: "shop.example.com", Range: "7d"})
	if !strings.Contains(string(b), "can't be read right now") {
		t.Error("an offline store isn't said")
	}
}
