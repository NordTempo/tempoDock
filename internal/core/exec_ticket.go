package core

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Exec tickets (ADR 0110): a browser can't send a bearer token with a
// WebSocket, and a WebSocket authenticated by the session cookie could be
// opened by any page (browsers don't apply CORS to them). So the UI asks
// for a ticket with an ordinary CSRF-guarded call, and the socket presents
// it: good once, for a short while, for the person and service it was
// made for. Tickets live in memory; a daemon restart drops them.

// maxExecTickets bounds the tickets waiting to be redeemed.
const maxExecTickets = 256

// execTickets are the tickets not yet redeemed, by value. Expired ones
// go when the next is made: there is no timer.
type execTickets struct {
	mu sync.Mutex
	m  map[string]execTicket
}

// execTicket is what a ticket stands for.
type execTicket struct {
	actor   Actor
	service int64
	expires time.Time
}

// ExecTicket checks what OpenExec will (admin on the environment, the
// lock, a running container) and returns a ticket for the actor's
// session in the service.
func (c *Core) ExecTicket(ctx context.Context, a Actor, ref ServiceRef) (apitypes.ExecTicket, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, "opening a shell")
	if err != nil {
		return apitypes.ExecTicket{}, err
	}
	rel, err := c.execRelease(ctx, ss)
	if err != nil {
		return apitypes.ExecTicket{}, err
	}
	t := &c.execTickets
	at := now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if !at.Before(v.expires) {
			delete(t.m, k)
		}
	}
	if len(t.m) >= maxExecTickets {
		return apitypes.ExecTicket{}, errf(apitypes.CodeRateLimited, "too many shells are being opened at once; try again in a minute")
	}
	if t.m == nil {
		t.m = map[string]execTicket{}
	}
	out := apitypes.ExecTicket{Ticket: rand.Text(), ExpiresAt: at.Add(apitypes.ExecTicketSeconds * time.Second), Container: shortID(rel.ContainerID), Release: rel.Number}
	t.m[out.Ticket] = execTicket{actor: a, service: ss.svc.ID, expires: out.ExpiresAt}
	return out, nil
}

// RedeemExecTicket returns the actor a ticket was made for, once, if it
// is for ref's service and hasn't expired. The session is then opened
// with OpenExec as that actor, which checks everything again.
func (c *Core) RedeemExecTicket(ctx context.Context, ticket string, ref ServiceRef) (Actor, error) {
	t := &c.execTickets
	t.mu.Lock()
	found, ok := t.m[ticket]
	delete(t.m, ticket)
	t.mu.Unlock()
	if !ok || !now().Before(found.expires) {
		return Actor{}, errf(apitypes.CodeUnauthorized, "the shell's ticket is unknown, used or expired; open the shell again")
	}
	ss, err := c.loadService(ctx, found.actor.OrgID, ref)
	if err != nil || ss.svc.ID != found.service {
		return Actor{}, errf(apitypes.CodeUnauthorized, "the shell's ticket is for another service; open the shell again")
	}
	return found.actor, nil
}
