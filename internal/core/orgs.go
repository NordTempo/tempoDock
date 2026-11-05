package core

import (
	"context"
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Organizations (ADR 0027): every project and manual token belongs to one,
// and every user acts in one at a time, with their role there.

// ListOrgs returns the orgs the actor can act in: a user's, or a manual
// token's own.
func (c *Core) ListOrgs(ctx context.Context, a Actor) ([]apitypes.Org, error) {
	if a.UserID == 0 {
		if a.OrgID == 0 {
			return []apitypes.Org{}, nil
		}
		o, err := c.store.GetOrg(ctx, a.OrgID)
		if err != nil {
			return nil, err
		}
		return []apitypes.Org{toOrg(o, a.Scope)}, nil
	}
	rows, err := c.store.ListUserOrgs(ctx, a.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Org, len(rows))
	for i, r := range rows {
		out[i] = toOrg(r.Org, apitypes.Scope(r.Role))
	}
	return out, nil
}

// CreateOrg creates an org with the calling user as its admin. Anyone with
// an account may; tokens may not, since an org needs a person in it.
func (c *Core) CreateOrg(ctx context.Context, a Actor, req apitypes.CreateOrgRequest) (apitypes.Org, error) {
	if a.UserID == 0 {
		return apitypes.Org{}, errf(apitypes.CodeForbidden, "%s can't create organizations; log in as a user", a)
	}
	if err := validName("organization", req.Slug); err != nil {
		return apitypes.Org{}, err
	}
	if slices.Contains(reservedOrgSlugs, req.Slug) {
		return apitypes.Org{}, invalid("%q is the address of a page in the web UI; pick another organization slug", req.Slug)
	}
	var org store.Org
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if org, err = createOrgTx(ctx, q, req.Slug, req.Name); err != nil {
			return err
		}
		return q.AddOrgMember(ctx, store.AddOrgMemberParams{OrgID: org.ID, UserID: a.UserID, Role: string(apitypes.ScopeAdmin)})
	})
	if store.IsUnique(err) {
		return apitypes.Org{}, conflict("organization %q already exists; pick another slug", req.Slug)
	}
	if err != nil {
		return apitypes.Org{}, err
	}
	c.audit(ctx, a.member(org, string(apitypes.ScopeAdmin), apitypes.ScopeAdmin), "org.create", nil, nil, org.Slug, "created organization "+org.Slug)
	return toOrg(org, apitypes.ScopeAdmin), nil
}

// createOrgTx creates an org with no members; name defaults to the slug.
func createOrgTx(ctx context.Context, q *store.Queries, slug, name string) (store.Org, error) {
	if name == "" {
		name = slug
	}
	return q.CreateOrg(ctx, store.CreateOrgParams{Slug: slug, Name: name})
}

func toOrg(o store.Org, role apitypes.Scope) apitypes.Org {
	return apitypes.Org{Slug: o.Slug, Name: o.Name, Role: role, CreatedAt: o.CreatedAt, RequireTwoFactor: o.RequireTwoFactor}
}
