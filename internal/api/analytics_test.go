package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Repeated filter and dimension parameters each count, as the UI and CLI
// send them: a second, unknown filter is refused, not dropped.
func TestAnalyticsRepeatedParameters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	web := core.ServiceRef{Project: "shop", Environment: "production", Service: "web"}
	if _, err := f.core.DeployImage(ctx, sys, web, apitypes.DeployImageRequest{Image: "app:1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.core.AddDomain(ctx, sys, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.core.EnableHostnameAnalytics(ctx, sys, "shop.example.com", apitypes.EnableAnalyticsRequest{}); err != nil {
		t.Fatal(err)
	}
	site := "/api/v1/projects/shop/sites/shop.example.com"
	if res, body := f.do("read", "GET", site+"/stats?filter=country:is:NO&filter=nope:is:x", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a second, unknown filter: %d %s", res.StatusCode, body)
	}
	if res, body := f.do("read", "GET", site+"/vitals?dimension=page&dimension=referrer", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a second dimension vitals can't break down by: %d %s", res.StatusCode, body)
	}
}
