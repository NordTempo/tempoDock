package core

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/traffic"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Traffic statistics (ADR 0033). Each server's Caddy sends the access log
// of service routes to the server's counter (internal/node/traffic). The
// minute tick drains every server's counts along with its resource
// samples, maps hostnames to services and, in one transaction, adds a
// minute point and the hour so far per service, and the hour's paths.
// When an hour is over its paths are trimmed to the top 100 and points
// past retention are pruned. Open pages patch from one traffic
// event per environment. Nothing runs that the stats tick doesn't already
// run, and a minute without requests writes nothing.

const (
	trafficHour      = 3600
	trafficKeep1m    = 24 * 60 * 60
	trafficKeepHours = 30 * 24 * 60 * 60
	// trafficTopPaths is how many paths ServiceTraffic lists.
	trafficTopPaths = 20
)

// trafficState is the collector's traffic memory; only the tick's
// goroutine uses it.
type trafficState struct {
	// hosts maps hostnames to their services, reloaded when a drained
	// hostname is missing.
	hosts map[string]store.ListTrafficHostsRow
	// hour is the hour paths are added to; when it changes, the hour
	// before is trimmed.
	hour int64
	// services are the current tick's counts per service.
	services map[int64]*serviceTraffic
}

// serviceTraffic is one service's traffic in a tick, added up over its
// hostnames.
type serviceTraffic struct {
	host   store.ListTrafficHostsRow
	counts node.HostTraffic          // without Paths
	paths  map[node.PathCount]uint64 // keyed without Requests
}

// loadHosts reads the hostname map again.
func (tr *trafficState) loadHosts(ctx context.Context, q *store.Queries) error {
	rows, err := q.ListTrafficHosts(ctx)
	if err != nil {
		return err
	}
	tr.hosts = make(map[string]store.ListTrafficHostsRow, len(rows))
	for _, r := range rows {
		tr.hosts[r.Hostname] = r
	}
	return nil
}

// addUp adds what one server drained to its services' counts. A hostname
// counts only for a service on that server, so a server can't report
// traffic for another's services.
func (tr *trafficState) addUp(ctx context.Context, q *store.Queries, serverID int64, hosts []node.HostTraffic, reloaded *bool) error {
	for _, h := range hosts {
		row, ok := tr.hosts[h.Host]
		if !ok && !*reloaded {
			*reloaded = true
			if err := tr.loadHosts(ctx, q); err != nil {
				return err
			}
			row, ok = tr.hosts[h.Host]
		}
		if !ok || row.ServerID != serverID {
			continue // a domain removed since, or not this server's
		}
		st := tr.services[row.ServiceID]
		if st == nil {
			st = &serviceTraffic{host: row, paths: map[node.PathCount]uint64{}}
			tr.services[row.ServiceID] = st
		}
		st.counts.Requests += h.Requests
		for i, n := range h.Status {
			st.counts.Status[i] += n
		}
		st.counts.Bytes += h.Bytes
		st.counts.Duration += h.Duration
		for _, p := range h.Paths[:min(len(h.Paths), traffic.DrainPaths)] {
			st.paths[node.PathCount{Method: p.Method, Path: p.Path}] += p.Requests
		}
	}
	return nil
}

// trafficTick stores and publishes the traffic the tick's samples
// drained. at is the start of the minute the counts are stored as.
func (c *Core) trafficTick(ctx context.Context, col *collector, now time.Time, at int64) {
	tr := &col.traffic
	if hour := at - at%trafficHour; hour != tr.hour {
		if err := c.trafficNewHour(ctx, hour, now.Unix()); err != nil {
			c.log.Warn("traffic: new hour", "err", err)
			return
		}
		tr.hour = hour
	}
	clear(tr.services)
	reloaded := false
	for i := range col.samples {
		s := &col.samples[i]
		if s.trafficErr != nil {
			c.log.Warn("traffic: drain", "server", s.srv.Name, "err", s.trafficErr)
			continue
		}
		if err := tr.addUp(ctx, c.store.Queries, s.srv.ID, s.traffic, &reloaded); err != nil {
			c.log.Warn("traffic: map hostnames", "err", err)
			return
		}
	}
	if len(tr.services) == 0 {
		return
	}

	points := make(map[int64]apitypes.TrafficPoint, len(tr.services))
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		for id, st := range tr.services {
			p := store.AddTrafficParams{
				ServiceID: id, Res: statsMinute, At: at, Requests: toInt64(st.counts.Requests),
				Status2xx: toInt64(st.counts.Status[0]), Status3xx: toInt64(st.counts.Status[1]),
				Status4xx: toInt64(st.counts.Status[2]), Status5xx: toInt64(st.counts.Status[3]),
				Bytes: toInt64(st.counts.Bytes), DurationMs: st.counts.Duration.Milliseconds(),
			}
			if err := q.AddTraffic(ctx, p); err != nil {
				return err
			}
			points[id] = trafficPoint(at, p.Requests, p.Status2xx, p.Status3xx, p.Status4xx, p.Status5xx, p.Bytes, p.DurationMs)
			p.Res, p.At = trafficHour, tr.hour
			if err := q.AddTraffic(ctx, p); err != nil {
				return err
			}
			for k, n := range st.paths {
				err := q.AddTrafficPath(ctx, store.AddTrafficPathParams{ServiceID: id, At: tr.hour, Method: k.Method, Path: k.Path, Requests: toInt64(n)})
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		c.log.Warn("traffic: store", "err", err)
		return
	}
	c.trafficErrors(tr.services, at)
	c.publishTraffic(tr, points, at, now)
}

// trafficNewHour starts a new hour: it trims the paths of the hour before
// and prunes what is past retention.
func (c *Core) trafficNewHour(ctx context.Context, hour, now int64) error {
	return c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.TrimTrafficPaths(ctx, hour-trafficHour); err != nil {
			return err
		}
		for _, p := range []store.PruneTrafficStatsParams{{Res: statsMinute, At: now - trafficKeep1m}, {Res: trafficHour, At: now - trafficKeepHours}} {
			if err := q.PruneTrafficStats(ctx, p); err != nil {
				return err
			}
		}
		return q.PruneTrafficPaths(ctx, now-trafficKeepHours)
	})
}

// publishTraffic sends one traffic event per environment with the minute
// points of its services, by service name.
func (c *Core) publishTraffic(tr *trafficState, points map[int64]apitypes.TrafficPoint, at int64, now time.Time) {
	byEnv := map[int64][]int64{}
	for id, st := range tr.services {
		byEnv[st.host.EnvironmentID] = append(byEnv[st.host.EnvironmentID], id)
	}
	point := time.Unix(at, 0).UTC()
	for _, ids := range byEnv {
		h := tr.services[ids[0]].host
		up := &apitypes.TrafficUpdate{Point: point, Services: make([]apitypes.ServiceTrafficPoint, len(ids))}
		for i, id := range ids {
			up.Services[i] = apitypes.ServiceTrafficPoint{Service: tr.services[id].host.ServiceName, Point: points[id]}
		}
		slices.SortFunc(up.Services, func(a, b apitypes.ServiceTrafficPoint) int { return strings.Compare(a.Service, b.Service) })
		c.bus.publish(busEvent{orgID: h.OrgID, projectID: h.ProjectID, envID: h.EnvironmentID, ev: apitypes.Event{
			Type: apitypes.EventTraffic, Time: now.UTC(), Project: h.ProjectName, Environment: h.EnvironmentName, Traffic: up,
		}})
	}
}

// trafficPoint builds a point from stored columns.
func trafficPoint(at, requests, s2, s3, s4, s5, bytes, durationMS int64) apitypes.TrafficPoint {
	return apitypes.TrafficPoint{Time: time.Unix(at, 0).UTC(), TrafficCounts: trafficCounts(requests, s2, s3, s4, s5, bytes, durationMS)}
}

// trafficCounts builds counts from stored columns, with the average
// duration per request.
func trafficCounts(requests, s2, s3, s4, s5, bytes, durationMS int64) apitypes.TrafficCounts {
	t := apitypes.TrafficCounts{
		Requests: requests, Status2xx: s2, Status3xx: s3, Status4xx: s4, Status5xx: s5, Bytes: bytes,
	}
	if requests > 0 {
		t.AvgDurationMS = float64(durationMS) / float64(requests)
	}
	return t
}

// trafficSpan maps a range to its resolution and start.
func trafficSpan(r apitypes.TrafficRange, now time.Time) (res, since int64, err error) {
	switch r {
	case apitypes.TrafficRange24h, "":
		return statsMinute, now.Unix() - trafficKeep1m, nil
	case apitypes.TrafficRange7d:
		return trafficHour, now.Unix() - 7*24*60*60, nil
	case apitypes.TrafficRange30d:
		return trafficHour, now.Unix() - trafficKeepHours, nil
	}
	return 0, 0, invalid("range must be 24h, 7d or 30d")
}

// ServiceTraffic returns what a service's domains served over a range:
// points, totals and the most requested paths.
func (c *Core) ServiceTraffic(ctx context.Context, a Actor, ref ServiceRef, r apitypes.TrafficRange) (apitypes.ServiceTraffic, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading service traffic")
	if err != nil {
		return apitypes.ServiceTraffic{}, err
	}
	res, since, err := trafficSpan(r, time.Now())
	if err != nil {
		return apitypes.ServiceTraffic{}, err
	}
	from := since - since%trafficHour
	id := ss.svc.ID
	rows, err := c.store.ListTrafficStats(ctx, store.ListTrafficStatsParams{ServiceID: id, Res: res, At: since})
	if err != nil {
		return apitypes.ServiceTraffic{}, err
	}
	tot, err := c.store.TrafficTotals(ctx, store.TrafficTotalsParams{ServiceID: id, At: from})
	if err != nil {
		return apitypes.ServiceTraffic{}, err
	}
	paths, err := c.store.TopTrafficPaths(ctx, store.TopTrafficPathsParams{ServiceID: id, At: from, Limit: trafficTopPaths})
	if err != nil {
		return apitypes.ServiceTraffic{}, err
	}

	out := apitypes.ServiceTraffic{
		Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		Range: cmp.Or(r, apitypes.TrafficRange24h), ResolutionSeconds: int(res), Since: time.Unix(from, 0).UTC(),
		Totals: trafficCounts(tot.Requests, tot.Status2xx, tot.Status3xx, tot.Status4xx, tot.Status5xx, tot.Bytes, tot.DurationMs),
		Points: make([]apitypes.TrafficPoint, len(rows)),
		Paths:  make([]apitypes.TrafficPath, len(paths)),
	}
	for i, p := range rows {
		out.Points[i] = trafficPoint(p.At, p.Requests, p.Status2xx, p.Status3xx, p.Status4xx, p.Status5xx, p.Bytes, p.DurationMs)
	}
	for i, p := range paths {
		out.Paths[i] = apitypes.TrafficPath{Method: p.Method, Path: p.Path, Requests: p.Requests}
	}
	return out, nil
}
