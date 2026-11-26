package core

import (
	"context"
	"slices"

	"github.com/NordTempo/tempoDock/internal/store"
)

// newSiteName is the name of a new site for a service (ADR 0104): its
// first custom domain that doesn't redirect, verified ones first, else
// the service's name (a generated hostname says nothing). createSite
// numbers a name the project's live sites took already. The name is a
// label: it stays when the domain goes.
func newSiteName(ctx context.Context, q *store.Queries, ss svcScope) (string, error) {
	doms, err := q.ListDomainsByService(ctx, ss.svc.ID)
	if err != nil {
		return "", err
	}
	doms = slices.DeleteFunc(doms, func(d store.Domain) bool { return d.Generated || d.RedirectTo != "" })
	slices.SortStableFunc(doms, func(a, b store.Domain) int {
		switch {
		case (a.VerifiedAt != nil) == (b.VerifiedAt != nil):
			return 0
		case a.VerifiedAt != nil:
			return -1
		}
		return 1
	})
	if len(doms) > 0 {
		return doms[0].Hostname, nil
	}
	return ss.svc.Name, nil
}
