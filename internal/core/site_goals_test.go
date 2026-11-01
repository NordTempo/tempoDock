package core

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func TestSiteGoalsAndFunnels(t *testing.T) {
	h := newHarness(t)
	site, svc, rel := countingSite(h)
	ms := now().UnixMilli()
	ev := func(v, k int, path, name string, off int64) []byte {
		return fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":%d,"p":%q,"n":%q,"v":%d}`, ms-10_000+off, site, svc, rel, k, path, name, v)
	}
	h.rt.AppendSpool(ev(1, 1, "/", "", 0), ev(1, 1, "/pricing", "", 1), ev(1, 2, "/pricing", "signup", 2),
		ev(2, 1, "/", "", 0), ev(2, 1, "/pricing", "", 1), ev(3, 1, "/", "", 0))
	eventually(t, "the events forwarded", func() bool { return h.rt.SpoolAcked() == 6 })

	dev := h.user("dev@x", apitypes.ScopeDeploy)
	reader := h.user("reader@x", apitypes.ScopeRead)
	if _, err := h.c.AddSiteGoal(h.ctx, reader, "shop", "shop.example.com", apitypes.CreateGoalRequest{Name: "Signup",
		GoalStep: apitypes.GoalStep{Kind: "event", Match: "signup"}}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a reader added a goal: %v", err)
	}
	for _, g := range []apitypes.CreateGoalRequest{
		{Name: "Signup", GoalStep: apitypes.GoalStep{Kind: "event", Match: "signup"}},
		{Name: "Pricing", GoalStep: apitypes.GoalStep{Kind: "page", Match: "/pric*"}},
	} {
		if _, err := h.c.AddSiteGoal(h.ctx, dev, "shop", "shop.example.com", g); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.c.AddSiteGoal(h.ctx, dev, "shop", "shop.example.com", apitypes.CreateGoalRequest{Name: "Signup",
		GoalStep: apitypes.GoalStep{Kind: "page", Match: "/x"}}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("a second goal named Signup: %v", err)
	}
	if _, err := h.c.AddSiteGoal(h.ctx, dev, "shop", "shop.example.com", apitypes.CreateGoalRequest{Name: "Bad",
		GoalStep: apitypes.GoalStep{Kind: "page", Match: "pricing"}}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a page goal without a /: %v", err)
	}
	goals, err := h.c.SiteGoals(h.ctx, reader, "shop", "shop.example.com", StatsQuery{Range: "24h"})
	if err != nil || len(goals.Goals) != 2 || goals.Visitors != 3 {
		t.Fatalf("goals = %+v, %v", goals, err)
	}
	if g := goals.Goals[0]; g.Name != "Signup" || g.Visitors != 1 || g.Rate < 0.33 || g.Rate > 0.34 {
		t.Errorf("signup = %+v", g)
	}
	if g := goals.Goals[1]; g.Visitors != 2 {
		t.Errorf("pricing = %+v", g)
	}

	if _, err := h.c.AddSiteFunnel(h.ctx, dev, "shop", "shop.example.com", apitypes.CreateFunnelRequest{Name: "Buy",
		Steps: []apitypes.GoalStep{{Kind: "page", Match: "/"}, {Kind: "page", Match: "/pricing"}, {Kind: "event", Match: "signup"}}}); err != nil {
		t.Fatal(err)
	}
	f, err := h.c.SiteFunnel(h.ctx, reader, "shop", "shop.example.com", "Buy", StatsQuery{Range: "24h"})
	if err != nil || len(f.Steps) != 3 || f.Steps[0].Sessions != 3 || f.Steps[1].Sessions != 2 || f.Steps[2].Sessions != 1 {
		t.Fatalf("funnel = %+v, %v", f, err)
	}
	if c := f.Steps[2].Conversion; c < 0.33 || c > 0.34 {
		t.Errorf("conversion to the last step = %v", c)
	}
	if err := h.c.RemoveSiteFunnel(h.ctx, dev, "shop", "shop.example.com", "Buy"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SiteFunnel(h.ctx, reader, "shop", "shop.example.com", "Buy", StatsQuery{}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("a removed funnel: %v", err)
	}
	if err := h.c.RemoveSiteGoal(h.ctx, dev, "shop", "shop.example.com", "Nope"); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("removing a goal that isn't: %v", err)
	}
}
