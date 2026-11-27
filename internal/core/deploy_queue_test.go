package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The deploy queue shows the release in the builder's slot and the ones
// waiting behind it in order. A reader limited to another project sees
// them as hidden in their place; the console names the builder and the
// org, and an operator cancels a waiting build, which leaves the line
// and the queue moves on.
func TestDeployQueue(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	api := h.service("api", apitypes.CreateServiceRequest{})
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "blog"}); err != nil {
		t.Fatal(err)
	}
	blog := ServiceRef{"blog", "production", "site"}
	if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"blog", "production"}, apitypes.CreateServiceRequest{Name: "site"}); err != nil {
		t.Fatal(err)
	}
	release := h.rt.HoldBuilds()
	defer release()
	queue := func(a Actor) apitypes.DeployQueue {
		t.Helper()
		q, err := h.c.DeployQueue(h.ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	waiting := func(q apitypes.DeployQueue) int {
		if len(q.Builders) == 0 {
			return -1
		}
		return len(q.Builders[0].Waiting)
	}
	var numbers []int64
	for i, ref := range []ServiceRef{web, blog, api} {
		r, err := h.c.DeploySource(h.ctx, testSystem, ref, sourceTar("v1"), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		numbers = append(numbers, r.Number)
		// In order: each joins the line once the one before is in it.
		eventually(t, "the release reaching the builder", func() bool {
			q := queue(testSystem)
			return len(q.Builders) == 1 && q.Builders[0].Building != nil && waiting(q) == i
		})
	}

	q := queue(testSystem)
	b := q.Builders[0]
	if b.Label != "Builder 1" || b.Name != "" || b.Building.Service != "web" || b.Building.Status != apitypes.ReleaseBuilding {
		t.Fatalf("builder = %+v, building %+v", b, b.Building)
	}
	if b.Waiting[0].Project != "blog" || b.Waiting[1].Service != "api" || b.Waiting[1].Release != numbers[2] || b.Waiting[1].CreatedBy != "system" {
		t.Fatalf("waiting = %+v", b.Waiting)
	}

	shopOnly := h.token(testSystem, apitypes.CreateTokenRequest{Name: "shop", Scope: apitypes.ScopeRead, Project: "shop"})
	got := queue(shopOnly).Builders[0].Waiting
	if !got[0].Hidden || got[0].Project != "" || got[1].Hidden || got[1].Service != "api" {
		t.Fatalf("shop's reader sees %+v", got)
	}

	op := h.operator("op@x")
	if _, err := h.c.OperatorDeployQueue(h.ctx, shopOnly); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org token reads the console's queue: %v", err)
	}
	cq, err := h.c.OperatorDeployQueue(h.ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	cb := cq.Builders[0]
	if cb.Name == "" || cb.Waiting[0].Org != "test" || cb.Waiting[0].ID == 0 {
		t.Fatalf("console builder = %+v", cb)
	}
	if _, err := h.c.OperatorCancelRelease(h.ctx, shopOnly, cb.Waiting[0].ID); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org token cancels from the console: %v", err)
	}
	canceled, err := h.c.OperatorCancelRelease(h.ctx, op, cb.Waiting[0].ID)
	if err != nil || canceled.Status != apitypes.ReleaseCanceled {
		t.Fatalf("operator cancel: %+v, %v", canceled, err)
	}
	if q := queue(testSystem); waiting(q) != 1 || q.Builders[0].Waiting[0].Service != "api" {
		t.Fatalf("after the cancel: %+v", q.Builders)
	}

	release()
	for i, ref := range []ServiceRef{web, api} {
		if r := h.wait(ref, numbers[i*2]); r.Status != apitypes.ReleaseActive {
			t.Fatalf("%s: %s %s", ref, r.Status, r.Error)
		}
	}
	if q := queue(testSystem); len(q.Builders) != 0 || len(q.Deploying) != 0 {
		t.Fatalf("an idle queue: %+v", q)
	}
}
