package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// ErrAccessChanged ends what an actor held open when their access ended
// or changed. Opening it again checks them as they are now, so the API
// ends such a stream as one that is over: a browser connects again by
// itself, and is refused if the access is gone.
var ErrAccessChanged = errf(apitypes.CodeUnauthorized, "your access changed while this was open; open it again, and sign in again if that is refused")

// accessChange says whose access ended or changed (ADR 0080's
// access.changed), for what they hold open to end on:
//   - tokenID: that token (revoked);
//   - userID: that person's sessions and `dock login` tokens, everywhere
//     or only in orgID, except token keepToken (the one that made the
//     change and stays);
//   - orgID alone: everyone whose access in the org comes from its
//     groups, grants, roles or agent rules.
//
// everyone, with orgID alone, is every actor acting in the org, its plain
// tokens too: the org was suspended or deleted.
//
// changed says the access may be more or less now, not gone (a new role,
// a group joined or left, a grant): live streams still end, to be opened
// again as the actor is now, and a run or a tunnel asks whether its
// actor may still hold it (heldOpen.still). An org's change always is.
type accessChange struct {
	userID, tokenID, orgID int64
	keepToken              int64
	changed, everyone      bool
	// envID: an environment's lock changed (ADR 0044). Only what is held
	// open in it with a still check is asked again, tokens' included.
	envID int64
}

// ends reports whether the change ends what a holds open. An org's
// change leaves its plain tokens alone: their scope is their own.
func (ch accessChange) ends(a Actor) bool {
	switch {
	case ch.tokenID != 0:
		return a.TokenID == ch.tokenID
	case ch.userID != 0:
		return a.UserID == ch.userID && (ch.orgID == 0 || a.OrgID == ch.orgID) && (ch.keepToken == 0 || a.TokenID != ch.keepToken)
	case ch.orgID != 0:
		return a.OrgID == ch.orgID && (ch.everyone || a.UserID != 0 || a.hasGrants || a.watchGrants || a.agent != nil)
	}
	return false
}

// accessWatch is what actors hold open past the call that checked them
// (live streams, attached runs, tunnels), each with how to end it.
// Nothing here polls: a write that ends or changes access calls
// Core.accessChanged, which ends the ones it matches.
type accessWatch struct {
	mu   sync.Mutex
	held map[*heldOpen]struct{}
	// asked counts the changes whose still checks are done, for tests.
	asked atomic.Int64
}

// heldOpen is one thing an actor holds open.
type heldOpen struct {
	a   Actor
	end func()
	// still, when set, checks the actor as they are now for what is open
	// (nil: they may still hold it). An org's change asks it instead of
	// ending: a run or a tunnel can't be opened again where it was.
	still func(context.Context, Actor) error
	// env is the environment it is open in, 0 for none: a change to
	// that environment's lock asks still again (accessChange.envID).
	env int64
}

// watch registers what a holds open; end closes it when a's access ends
// or changes, at most once and without blocking. still (nil: none) is
// asked first when the access changed without ending (accessChange). stop takes it out. The system holds nothing: its access
// doesn't end.
func (w *accessWatch) watch(a Actor, env int64, end func(), still func(context.Context, Actor) error) (stop func()) {
	if a.UserID == 0 && a.TokenID == 0 {
		return func() {}
	}
	h := &heldOpen{a: a, end: sync.OnceFunc(end), still: still, env: env}
	w.mu.Lock()
	if w.held == nil {
		w.held = map[*heldOpen]struct{}{}
	}
	w.held[h] = struct{}{}
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		delete(w.held, h)
		w.mu.Unlock()
	}
}

// accessChanged ends everything open that ch's actors hold. Call it
// after the write is committed, so opening again meets the new state.
func (c *Core) accessChanged(ch accessChange) {
	w := &c.access
	changed := ch.changed || ch.userID == 0 && ch.tokenID == 0
	w.mu.Lock()
	var ends []func()
	var ask []*heldOpen
	for h := range w.held {
		switch {
		case ch.envID != 0:
			if h.env == ch.envID && h.still != nil {
				ask = append(ask, h)
			}
		case !ch.ends(h.a):
		case changed && h.still != nil:
			ask = append(ask, h)
		default:
			ends = append(ends, h.end)
		}
	}
	w.mu.Unlock()
	for _, end := range ends {
		end()
	}
	if len(ask) == 0 {
		return
	}
	// Off the writer's path: each check reads the store.
	c.goCounted("checking who still has access", func() {
		defer w.asked.Add(1)
		for _, h := range ask {
			// A check that can't finish ends what it guards, as a
			// refusal does.
			err := caughtErr(c.log, "checking who still has access", func() error {
				now, err := c.actorNow(c.ctx, h.a)
				if err != nil {
					return err
				}
				return h.still(c.ctx, now)
			})
			if err != nil {
				h.end()
			}
		}
	})
}

// actorNow is a's credential as an actor in the same org now: what its
// token, its person, their membership and their grants say today. One
// call a request approved (granted) keeps that approval. Any doubt is an
// error, and what asked closes.
func (c *Core) actorNow(ctx context.Context, a Actor) (Actor, error) {
	var now Actor
	switch {
	case a.TokenID != 0:
		row, err := c.store.GetTokenByIDWithOwner(ctx, a.TokenID)
		if err != nil {
			return Actor{}, err
		}
		if now, err = c.tokenActor(ctx, row, a.OrgSlug); err != nil {
			return Actor{}, err
		}
	default:
		u, err := c.store.GetUser(ctx, a.UserID)
		if err != nil {
			return Actor{}, err
		}
		if u.DisabledAt != nil {
			return Actor{}, errUnauthenticated
		}
		now = userActor(u)
		now.SessionID, now.Factor, now.VerifiedAt = a.SessionID, a.Factor, a.VerifiedAt
		now.SSOProviderID, now.SSOProvider, now.SSOLabel = a.SSOProviderID, a.SSOProvider, a.SSOLabel
		if now, err = c.actIn(ctx, c.gateOperator(now), a.OrgSlug, apitypes.ScopeAdmin); err != nil {
			return Actor{}, err
		}
	}
	if now.OrgID != a.OrgID {
		return Actor{}, ErrAccessChanged
	}
	now.granted, now.approval, now.ownerApproved = a.granted, a.approval, a.ownerApproved
	return now, nil
}

// watched is ctx ending with ErrAccessChanged when a's access ends or
// changes (see watch for still), or, with env, that environment's lock
// changes and still refuses; stop frees it.
func (c *Core) watched(ctx context.Context, a Actor, env int64, still func(context.Context, Actor) error) (_ context.Context, stop func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	unwatch := c.access.watch(a, env, func() { cancel(ErrAccessChanged) }, still)
	return ctx, func() {
		unwatch()
		cancel(nil)
	}
}

// held is stream s ending with ErrAccessChanged when a's access ends or
// changes while it is open. Every operation returning a Stream returns it
// through held (TestEveryStreamIsHeld): the checks before it are of the
// actor as they were when it opened.
func held[T any](c *Core, a Actor, s Stream[T]) Stream[T] {
	return func(ctx context.Context, ready func(), send func(...T) error) error {
		ctx, stop := c.watched(ctx, a, 0, nil)
		defer stop()
		err := s(ctx, ready, send)
		if errors.Is(context.Cause(ctx), ErrAccessChanged) {
			return ErrAccessChanged
		}
		return err
	}
}

// counted is stream s taking one of a's maxActorStreams places while it
// runs (streamCounts); past the bound it is refused before it is live.
func counted[T any](c *Core, a Actor, s Stream[T]) Stream[T] {
	return func(ctx context.Context, ready func(), send func(...T) error) error {
		closed, err := c.streams.open(a)
		if err != nil {
			return err
		}
		defer closed()
		return s(ctx, ready, send)
	}
}
