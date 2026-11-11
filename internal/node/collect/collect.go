// Package collect is a server's node.Analytics (ADR 0037): its spool and
// the salts its ingest hashes visitors with, and, on the analytics server,
// the store's container.
package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
	"github.com/NordTempo/tempoDock/internal/analytics/spool"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/traces"
)

// Store runs the analytics store's container (docker.AnalyticsStore).
type Store interface {
	Ensure(ctx context.Context, spec node.AnalyticsStoreSpec) error
	Remove(ctx context.Context) error
	Dial(ctx context.Context) (net.Conn, error)
}

// Collector implements node.Analytics. Spool, Replay and Salts are
// required; Store is nil on a server that can't run containers of its own
// (the fake runtime).
type Collector struct {
	Spool *spool.Spool
	// Replay is the replay spool (node.SpoolReplay), and Spans the spans
	// spool (node.SpoolSpans) the trace ingest fills; nil where there is
	// none.
	Replay *spool.Spool
	Spans  *spool.Spool
	// TraceKey is the key the trace ingest checks tokens with;
	// SetAnalytics replaces it.
	TraceKey *traces.Key
	Salts    *ingest.Salts
	// Rules are the sites' rules the ingest applies; SetAnalytics
	// replaces them.
	Rules *ingest.Rules
	// Store runs the analytics store and Traces the trace store.
	Store, Traces Store
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

var _ node.Analytics = (*Collector)(nil)

// batchWait is how long new events gather before a batch goes out once
// the tail has caught up: the plan's "up to 1,000 events or 1 s".
const batchWait = time.Second

// SetAnalytics implements node.Analytics.
func (c *Collector) SetAnalytics(_ context.Context, cfg node.AnalyticsConfig) error {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	// Salts first: without them nothing is counted, while a rules error
	// only keeps the old rules.
	if err := c.Salts.Set(cfg.Salts, now()); err != nil {
		return err
	}
	if c.TraceKey != nil {
		c.TraceKey.Set(cfg.TraceKey)
	}
	if c.Rules == nil || cfg.Sites == nil {
		return nil
	}
	return c.Rules.Set(cfg.Sites)
}

// TailSpool implements node.Analytics. While behind it reads batch after
// batch; once caught up it waits for an append (no polling), then gives
// later events a second to join the batch: the one timer, armed only
// while events wait to go out.
//
// after is the store's cursor: a spool behind it (the end of a segment
// lost to a power loss, or a reset spool) continues past it first, so
// new events aren't taken for ones the store has.
func (c *Collector) TailSpool(ctx context.Context, which node.Spool, after uint64) (<-chan node.SpoolBatch, error) {
	sp, err := c.spool(which)
	if err != nil {
		return nil, err
	}
	if err := sp.Resume(after); err != nil {
		return nil, err
	}
	out := make(chan node.SpoolBatch, 1)
	go func() {
		defer close(out)
		pos := after
		for {
			b, err := sp.Read(pos, node.SpoolBatchEvents)
			if err != nil {
				return
			}
			if len(b.Lines) == 0 {
				pos = b.Position
				if sp.Wait(ctx, pos) != nil {
					return
				}
				t := time.NewTimer(batchWait)
				select {
				case <-t.C:
				case <-ctx.Done():
					t.Stop()
					return
				}
				continue
			}
			batch := node.SpoolBatch{Position: b.Position, Events: bytes.Join(b.Lines, []byte{'\n'}), Count: len(b.Lines), Skipped: b.Skipped}
			select {
			case out <- batch:
			case <-ctx.Done():
				return
			}
			pos = b.Position
		}
	}()
	return out, nil
}

// AckSpool implements node.Analytics.
func (c *Collector) AckSpool(_ context.Context, which node.Spool, position uint64, events int) error {
	sp, err := c.spool(which)
	if err != nil {
		return err
	}
	return sp.Ack(position, events)
}

// spool is the named spool.
func (c *Collector) spool(which node.Spool) (*spool.Spool, error) {
	switch which {
	case node.SpoolEvents:
		return c.Spool, nil
	case node.SpoolReplay:
		if c.Replay != nil {
			return c.Replay, nil
		}
	case node.SpoolSpans:
		if c.Spans != nil {
			return c.Spans, nil
		}
	}
	return nil, fmt.Errorf("this server has no %q spool", which)
}

// errNoStore is the answer of a server that can't run a store.
var errNoStore = errors.New("this server can't run the analytics or trace store: it has no Docker runtime")

// store is the Store of kind, nil if this server has none.
func (c *Collector) store(kind node.StoreKind) Store {
	if kind == node.StoreTraces {
		return c.Traces
	}
	return c.Store
}

// EnsureAnalyticsStore implements node.Analytics.
func (c *Collector) EnsureAnalyticsStore(ctx context.Context, spec node.AnalyticsStoreSpec) error {
	s := c.store(spec.Kind)
	if s == nil {
		return errNoStore
	}
	return s.Ensure(ctx, spec)
}

// RemoveAnalyticsStore implements node.Analytics: the container and its
// data. The binary is the caller's: the control plane's staged copy, or
// the one the node link received.
func (c *Collector) RemoveAnalyticsStore(ctx context.Context, kind node.StoreKind) error {
	s := c.store(kind)
	if s == nil {
		return nil
	}
	return s.Remove(ctx)
}

// DialAnalyticsStore implements node.Analytics.
func (c *Collector) DialAnalyticsStore(ctx context.Context, kind node.StoreKind) (net.Conn, error) {
	s := c.store(kind)
	if s == nil {
		return nil, errNoStore
	}
	return s.Dial(ctx)
}
