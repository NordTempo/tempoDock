package core

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cloudflare"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Redirects on Cloudflare zones (ADR 0068): a hostname in one of a
// connection's zones that Cloudflare redirects to a URL at its edge, with
// no service or server behind it. Dock makes two things for one, both
// its own: a proxied placeholder record (AAAA 100::, the discard prefix;
// no request reaches it) so the hostname goes through Cloudflare, and a
// Single Redirect rule in the zone's entry point ruleset. It adds,
// changes and deletes its own rules one at a time and never writes the
// ruleset back whole, so rules made in the dashboard stay.
//
// A redirect is its connection's owner's: the platform's, for operators,
// or an org's, for its unlimited admins. A platform zone shared with orgs
// keeps its redirects on the platform's level. The rows of
// cloudflare_redirects are worked on by the Cloudflare worker
// (cloudflare_queue.go), like the records of domains.

const (
	// redirectPlaceholder is the content of the record Dock makes for a
	// redirect's hostname.
	redirectPlaceholder = "100::"
	// redirectTargetMax caps a redirect's target URL.
	redirectTargetMax = 2048
	// cfRedirectsCap caps the list of a level's redirects.
	cfRedirectsCap = 1000
)

// redirectCodes are the status codes a redirect may answer with.
var redirectCodes = []int{301, 302, 307, 308}

// ListCloudflareRedirects returns the org's redirects (with platform,
// the platform's), by hostname.
func (c *Core) ListCloudflareRedirects(ctx context.Context, a Actor, platform bool) ([]apitypes.CloudflareRedirect, error) {
	org, err := connectionsIn(a, platform, "listing redirects")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListCloudflareRedirectsOf(ctx, store.ListCloudflareRedirectsOfParams{Org: org, Max: cfRedirectsCap})
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.CloudflareRedirect, len(rows))
	for i, r := range rows {
		out[i] = c.toRedirect(r.CloudflareRedirect, r.ConnectionName)
	}
	return out, nil
}

// AddCloudflareRedirect adds a redirect for a hostname in one of the
// level's own zones. Cloudflare is asked first, so a record in the way
// fails here; the worker then makes the record and the rule.
func (c *Core) AddCloudflareRedirect(ctx context.Context, a Actor, platform bool, req apitypes.CreateCloudflareRedirectRequest) (apitypes.CloudflareRedirect, error) {
	org, err := connectionsIn(a, platform, "adding redirects")
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	host, err := normalizeHostname(req.Hostname)
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	code := req.StatusCode
	if code == 0 {
		code = 302
	}
	target, err := checkRedirect(host, req.Target, code, req.KeepPath)
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	if err := c.checkRedirectHost(host); err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	z, err := c.redirectZone(org, host, req.Connection)
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	conn, err := c.store.GetCloudflareConnectionByID(ctx, z.ConnectionID)
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	if err := c.checkRedirectName(ctx, conn, z, host); err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	t := now()
	var row store.CloudflareRedirect
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := checkRedirectFree(ctx, q, host); err != nil {
			return err
		}
		row, err = q.CreateCloudflareRedirect(ctx, store.CreateCloudflareRedirectParams{
			Hostname: host, ConnectionID: conn.ID, ZoneID: z.ZoneID, Target: target, StatusCode: int64(code),
			KeepPath: req.KeepPath, CreatedAt: t, UpdatedAt: t, UpdatedBy: a.Name,
		})
		return err
	})
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	c.audit(ctx, cfAudit(a, org), "cloudflare.redirect_add", nil, nil, host, fmt.Sprintf("added redirect %s to %s (%d%s)", host, target, code, keepPathNote(req.KeepPath)))
	c.kickCloudflare()
	return c.toRedirect(row, conn.Name), nil
}

// UpdateCloudflareRedirect changes one of the level's redirects: its
// target, status code or whether it keeps the path. Cloudflare's rule is
// changed to match.
func (c *Core) UpdateCloudflareRedirect(ctx context.Context, a Actor, platform bool, hostname string, req apitypes.UpdateCloudflareRedirectRequest) (apitypes.CloudflareRedirect, error) {
	org, err := connectionsIn(a, platform, "changing redirects")
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	cur, conn, err := c.cloudflareRedirect(ctx, org, hostname)
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	if cur.Want == cfAbsent {
		return apitypes.CloudflareRedirect{}, conflict("redirect %s is being removed; add it again once it is gone", cur.Hostname)
	}
	target, code, keep := cur.Target, int(cur.StatusCode), cur.KeepPath
	patch(&target, req.Target)
	patch(&code, req.StatusCode)
	patch(&keep, req.KeepPath)
	if target, err = checkRedirect(cur.Hostname, target, code, keep); err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	row, err := c.store.UpdateCloudflareRedirectSettings(ctx, store.UpdateCloudflareRedirectSettingsParams{
		Target: target, StatusCode: int64(code), KeepPath: keep, UpdatedAt: now(), UpdatedBy: a.Name, ID: cur.ID,
	})
	if store.IsNotFound(err) {
		return apitypes.CloudflareRedirect{}, conflict("redirect %s is being removed; add it again once it is gone", cur.Hostname)
	}
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	c.audit(ctx, cfAudit(a, org), "cloudflare.redirect_update", nil, nil, row.Hostname, fmt.Sprintf("changed redirect %s: to %s (%d%s)", row.Hostname, target, code, keepPathNote(keep)))
	c.kickCloudflare()
	return c.toRedirect(row, conn), nil
}

// RemoveCloudflareRedirect removes one of the level's redirects: the
// worker deletes its rule and record in Cloudflare, then forgets it.
func (c *Core) RemoveCloudflareRedirect(ctx context.Context, a Actor, platform bool, hostname string) error {
	org, err := connectionsIn(a, platform, "removing redirects")
	if err != nil {
		return err
	}
	cur, _, err := c.cloudflareRedirect(ctx, org, hostname)
	if err != nil {
		return err
	}
	if err := c.store.SetCloudflareRedirectAbsent(ctx, store.SetCloudflareRedirectAbsentParams{UpdatedAt: now(), UpdatedBy: a.Name, ID: cur.ID}); err != nil {
		return err
	}
	c.audit(ctx, cfAudit(a, org), "cloudflare.redirect_remove", nil, nil, cur.Hostname, "removed redirect "+cur.Hostname+" to "+cur.Target)
	c.kickCloudflare()
	return nil
}

// RetryCloudflareRedirect has the worker work on one of the level's
// redirects again, after what failed it was fixed.
func (c *Core) RetryCloudflareRedirect(ctx context.Context, a Actor, platform bool, hostname string) (apitypes.CloudflareRedirect, error) {
	org, err := connectionsIn(a, platform, "retrying redirects")
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	cur, conn, err := c.cloudflareRedirect(ctx, org, hostname)
	if err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	t := now()
	if err := c.store.RetryCloudflareRedirect(ctx, store.RetryCloudflareRedirectParams{UpdatedAt: t, ID: cur.ID}); err != nil {
		return apitypes.CloudflareRedirect{}, err
	}
	c.kickCloudflare()
	cur.Status, cur.Reason, cur.UpdatedAt = "pending", "", t
	return c.toRedirect(cur, conn), nil
}

// cloudflareRedirect loads one of org's redirects (0: the platform's) by
// hostname, with its connection's name.
func (c *Core) cloudflareRedirect(ctx context.Context, org int64, hostname string) (store.CloudflareRedirect, string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	row, err := c.store.GetCloudflareRedirectOf(ctx, store.GetCloudflareRedirectOfParams{Hostname: host, Org: org})
	if store.IsNotFound(err) {
		return store.CloudflareRedirect{}, "", notFound("no redirect for %s; `dock cloudflare redirects%s` lists them", host, cfFlag(orgRef(org)))
	}
	return row.CloudflareRedirect, row.ConnectionName, err
}

// checkRedirect checks a redirect's target, status code and keep_path
// for host, and returns the target as saved. The target goes into a
// rule's expression, so it may hold no quote or backslash.
func checkRedirect(host, target string, code int, keepPath bool) (string, error) {
	t := strings.TrimSpace(target)
	switch {
	case !slices.Contains(redirectCodes, code):
		return "", invalid("a redirect's status code is 301, 302, 307 or 308, not %d", code)
	case t == "":
		return "", invalid("a redirect needs a target URL, like https://instagram.com/tempo")
	case len(t) > redirectTargetMax:
		return "", invalid("the target URL is longer than %d characters", redirectTargetMax)
	case strings.ContainsFunc(t, func(r rune) bool { return r == '"' || r == '\\' || unicode.IsSpace(r) || !unicode.IsPrint(r) }):
		return "", invalid("the target URL can't hold quotes, backslashes or spaces; percent-encode them")
	}
	u, err := url.Parse(t)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return "", invalid("the target must be a full URL starting with https:// or http://, like https://instagram.com/tempo")
	case u.User != nil:
		return "", invalid("the target URL can't hold a user name or password")
	case strings.EqualFold(u.Hostname(), host):
		return "", invalid("a redirect can't go to its own hostname")
	case keepPath && (u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(t, "?") || strings.HasSuffix(t, "#")):
		return "", invalid("to keep the path, the target can't have a ? or #: the path and query are added at its end")
	}
	return t, nil
}

// checkRedirectHost refuses hostnames Dock answers on itself.
func (c *Core) checkRedirectHost(host string) error {
	switch {
	case host == c.cfg.Hooks.PublicHost:
		return errf(apitypes.CodeHostnameTaken, "%s is this server's webhook hostname ([hooks] public_host); use another hostname", host)
	case host == c.publicHost():
		return errf(apitypes.CodeHostnameTaken, "%s is Dock's own address (`dock address`); use another hostname", host)
	case c.publicSite(host) != 0:
		return errf(apitypes.CodeHostnameTaken, "%s is a site's public dashboard (`dock analytics site`); use another hostname", host)
	}
	return nil
}

// checkRedirectFree refuses a hostname something else holds, in q's
// transaction: a domain (verified or not, in any org), a record Dock
// still routes or is deleting, or another redirect.
func checkRedirectFree(ctx context.Context, q *store.Queries, host string) error {
	if taken, err := q.HostnameExists(ctx, host); err != nil {
		return err
	} else if taken {
		return errf(apitypes.CodeHostnameTaken, "%s is a service's domain; remove the domain from its service first", host)
	}
	if _, err := q.GetCloudflareRecord(ctx, host); err == nil {
		return errf(apitypes.CodeHostnameTaken, "Dock still routes %s through a tunnel, or is deleting its record; try again once its domain is gone", host)
	} else if !store.IsNotFound(err) {
		return err
	}
	if taken, err := q.RedirectHostnameExists(ctx, host); err != nil {
		return err
	} else if taken {
		return conflict("a redirect for %s exists; change it with `dock cloudflare redirects set %s`", host, host)
	}
	return nil
}

// redirectZone is the zone host is in among org's own connections' (0:
// the platform's), never a zone org only uses through a shared platform
// connection: redirects are on the level a zone is added. conn, when
// set, picks the connection; otherwise the closest zone of the oldest
// connection wins (toZones' order).
func (c *Core) redirectZone(org int64, host, conn string) (store.CloudflareZone, error) {
	c.cf.mu.Lock()
	defer c.cf.mu.Unlock()
	for _, z := range c.cf.zones {
		if z.org == org && inZone(host, z.Name) && (conn == "" || c.cf.conns[z.ConnectionID] == conn) {
			return z.CloudflareZone, nil
		}
	}
	level := "this organization's own Cloudflare connections"
	if org == 0 {
		level = "the platform's Cloudflare connections"
	}
	if conn != "" {
		return store.CloudflareZone{}, invalid("%s isn't in a zone of Cloudflare connection %s, among %s", host, conn, level)
	}
	return store.CloudflareZone{}, invalid("%s isn't in a zone of %s; a redirect is added on the level its zone is added: give one of them a token that reaches the zone", host, level)
}

// checkRedirectName asks Cloudflare for records named host that Dock
// didn't make and that would be in the way. Cloudflare not answering in
// time isn't an error: the worker checks again.
func (c *Core) checkRedirectName(ctx context.Context, conn store.CloudflareConnection, z store.CloudflareZone, host string) error {
	if conn.Status != string(apitypes.CloudflareOK) {
		return invalid("Cloudflare connection %s doesn't work (%s); give it a new token with `dock cloudflare set %s%s --token-stdin`", conn.Name, conn.StatusReason, conn.Name, cfFlag(conn.OrgID))
	}
	cl, err := c.cfClient(conn)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfNameCheckTimeout)
	defer cancel()
	existing, err := cl.RecordsNamed(ctx, z.ZoneID, host)
	if e, ok := cloudflare.AsError(err); ok && e.Temporary() || ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return invalid("could not look up %s in Cloudflare: %v", host, err)
	}
	if way := c.recordsInWay(existing, ""); len(way) > 0 {
		return redirectInWay(host, way)
	}
	return nil
}

// redirectInWay is why a redirect can't be made: records Dock didn't make.
func redirectInWay(host string, way []cloudflare.Record) error {
	return errf(apitypes.CodeHostnameTaken, "%s already has %s in Cloudflare, which Dock didn't make and won't change; delete it there first", host, describeRecords(way))
}

// describeRedirects names a connection's redirects for a message: "2
// redirects (a.tempo.run, b.tempo.run)", at most five by name.
func describeRedirects(rows []store.CloudflareRedirect) string {
	names := make([]string, 0, 5)
	for _, r := range rows[:min(len(rows), 5)] {
		names = append(names, r.Hostname)
	}
	if len(rows) > 5 {
		names = append(names, fmt.Sprintf("and %d more", len(rows)-5))
	}
	noun := "redirects"
	if len(rows) == 1 {
		noun = "redirect"
	}
	return fmt.Sprintf("%d %s (%s)", len(rows), noun, strings.Join(names, ", "))
}

// keepPathNote is what an audit summary says of keep_path.
func keepPathNote(keep bool) string {
	if keep {
		return ", keeping the path"
	}
	return ""
}

// toRedirect is a redirect as the API shows it.
func (c *Core) toRedirect(r store.CloudflareRedirect, conn string) apitypes.CloudflareRedirect {
	status := apitypes.RedirectStatus(r.Status)
	switch {
	case r.Want == cfAbsent:
		status = apitypes.RedirectDeleting
	case r.Status == "ok":
		status = apitypes.RedirectLive
	}
	return apitypes.CloudflareRedirect{
		Hostname: r.Hostname, Zone: c.zoneName(r.ZoneID), Connection: conn, Target: r.Target, StatusCode: int(r.StatusCode),
		KeepPath: r.KeepPath, Status: status, Reason: r.Reason, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy,
	}
}

// publishRedirect sends a redirect whose state changed to those who see
// its level: operators the platform's, an org's admins the org's.
func (c *Core) publishRedirect(ctx context.Context, id int64) {
	r, err := c.store.GetCloudflareRedirectByID(ctx, id)
	if err != nil {
		return
	}
	c.publishRedirectView(r.OrgID, c.toRedirect(r.CloudflareRedirect, r.ConnectionName), false)
}

// publishRedirectView sends a redirect of org's (0: the platform's); with
// removed, that it is gone.
func (c *Core) publishRedirectView(org int64, r apitypes.CloudflareRedirect, removed bool) {
	ev := apitypes.Event{Type: apitypes.EventCloudflareRedirect, Redirect: &r}
	if removed {
		ev.Message = "removed"
	}
	c.bus.publish(busEvent{orgID: org, serverStats: org == 0, adminsOnly: org != 0, ev: ev})
}

// strandedRedirectReason is why a redirect whose connection lost its
// zone fails.
func strandedRedirectReason(conn string) string {
	return fmt.Sprintf("Cloudflare connection %s no longer reaches this redirect's zone; give its token the zone again and refresh the connection, "+
		"or remove the redirect (what Dock made then stays in Cloudflare)", conn)
}

// cloudflareRedirects works through the due redirects, in batches.
func (c *Core) cloudflareRedirects(ctx context.Context, conns map[int64]*cfConn) error {
	for range cfMaxBatches {
		due, err := c.store.ListDueCloudflareRedirects(ctx, store.ListDueCloudflareRedirectsParams{Now: ptr(now()), Max: cfBatch})
		if err != nil {
			return err
		}
		for _, r := range due {
			cc, err := c.conn(ctx, conns, r.ConnectionID)
			if err != nil {
				return err
			}
			if cc.row.Status != string(apitypes.CloudflareOK) {
				continue
			}
			if err := c.redirectStep(ctx, cc, r); err != nil {
				return err
			}
		}
		if len(due) < cfBatch {
			return nil
		}
	}
	return nil
}

// redirectStep makes Cloudflare match one redirect and saves the result.
// Only a failure to save (or a rate limit) is returned. It holds
// redirectsMu, so a connection's forced removal never deletes a rule
// while a step makes it again.
func (c *Core) redirectStep(ctx context.Context, cc *cfConn, r store.CloudflareRedirect) error {
	c.cf.redirectsMu.Lock()
	defer c.cf.redirectsMu.Unlock()
	if !c.holdsZone(r.ConnectionID, r.ZoneID) {
		// Its connection can't reach the zone: a removal forgets it, what
		// Dock made stays; anything else fails until the zone is back.
		if r.Want == cfAbsent {
			return c.forgetRedirect(ctx, cc, r)
		}
		if _, err := c.store.SaveCloudflareRedirectResult(ctx, store.SaveCloudflareRedirectResultParams{
			RecordID: r.RecordID, RulesetID: r.RulesetID, RuleID: r.RuleID, Status: "error", Reason: strandedRedirectReason(cc.row.Name),
			UpdatedAt: now(), ID: r.ID, Gen: r.Gen,
		}); err != nil {
			return err
		}
		c.publishRedirect(ctx, r.ID)
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, cfCallTimeout)
	defer cancel()
	if r.Want == cfAbsent {
		if err := c.unredirect(cctx, cc.client, r); err != nil {
			return c.redirectFailed(ctx, cc, r, err)
		}
		return c.forgetRedirect(ctx, cc, r)
	}
	res, err := c.redirect(cctx, cc.client, r)
	if err != nil {
		return c.redirectFailed(ctx, cc, r, err)
	}
	saved, err := c.store.SaveCloudflareRedirectResult(ctx, store.SaveCloudflareRedirectResultParams{
		RecordID: res.recordID, RulesetID: res.rulesetID, RuleID: res.ruleID, Status: res.status, Reason: res.reason,
		UpdatedAt: now(), ID: r.ID, Gen: r.Gen,
	})
	if err != nil || saved == 0 {
		return err // saved == 0: changed meanwhile; the next step redoes it
	}
	c.publishRedirect(ctx, r.ID)
	return nil
}

// forgetRedirect deletes a removed redirect's row and tells clients.
func (c *Core) forgetRedirect(ctx context.Context, cc *cfConn, r store.CloudflareRedirect) error {
	if err := c.store.DeleteCloudflareRedirect(ctx, store.DeleteCloudflareRedirectParams{ID: r.ID, Gen: r.Gen}); err != nil {
		return err
	}
	c.publishRedirectView(cfOwner(cc.row), c.toRedirect(r, cc.row.Name), true)
	return nil
}

// redirectFailed saves what a failed redirect step means.
func (c *Core) redirectFailed(ctx context.Context, cc *cfConn, r store.CloudflareRedirect, stepErr error) error {
	perm := "Zone › Single Redirect › Edit and Zone › DNS › Edit on " + c.zoneName(r.ZoneID)
	out, err := c.failed(ctx, cc, r.Attempts, stepErr, perm)
	if out.status == "" {
		return err
	}
	if _, uerr := c.store.SaveCloudflareRedirectResult(ctx, store.SaveCloudflareRedirectResultParams{
		RecordID: r.RecordID, RulesetID: r.RulesetID, RuleID: r.RuleID, Status: out.status, Reason: out.reason,
		Attempts: out.attempts, RetryAt: out.retryAt, UpdatedAt: now(), ID: r.ID, Gen: r.Gen,
	}); uerr != nil {
		return uerr
	}
	c.publishRedirect(ctx, r.ID)
	return err
}

// redirectResult is what redirect found or did: the IDs of what Dock
// made, and status ok, or error with the reason.
type redirectResult struct {
	recordID, rulesetID, ruleID string
	status, reason              string
}

// redirect makes Cloudflare redirect r.Hostname: the placeholder record
// (any other record of Dock's for the name goes; one Dock didn't make is
// in the way, and Dock never touches it), then the rule, added, changed
// or kept as it is.
func (c *Core) redirect(ctx context.Context, cl *cloudflare.Client, r store.CloudflareRedirect) (redirectResult, error) {
	existing, err := cl.RecordsNamed(ctx, r.ZoneID, r.Hostname)
	if err != nil {
		return redirectResult{}, err
	}
	if way := c.recordsInWay(existing, ""); len(way) > 0 {
		return redirectResult{status: "error", reason: redirectInWay(r.Hostname, way).Error()}, nil
	}
	res := redirectResult{status: "ok"}
	for _, e := range existing {
		if e.Comment != c.recordComment() {
			continue
		}
		if res.recordID == "" && e.Type == "AAAA" && e.Content == redirectPlaceholder && e.Proxied {
			res.recordID = e.ID
			continue
		}
		if err := cl.DeleteRecord(ctx, r.ZoneID, e.ID); err != nil {
			return redirectResult{}, err
		}
	}
	if res.recordID == "" {
		rec, err := cl.CreateRecord(ctx, r.ZoneID, cloudflare.Record{
			Type: "AAAA", Name: r.Hostname, Content: redirectPlaceholder, Proxied: true, TTL: 1, Comment: c.recordComment(),
		})
		if err != nil {
			return redirectResult{}, err
		}
		res.recordID = rec.ID
	}
	want := c.redirectRule(r)
	rs, err := cl.EntrypointRuleset(ctx, r.ZoneID, cloudflare.RedirectPhase)
	if err != nil {
		return redirectResult{}, err
	}
	var after cloudflare.Ruleset
	switch have := c.findRule(rs, r); {
	case rs == nil:
		after, err = cl.CreateRuleset(ctx, r.ZoneID, cloudflare.Ruleset{Name: "Redirect rules", Phase: cloudflare.RedirectPhase, Rules: []cloudflare.Rule{want}})
	case have == nil:
		after, err = cl.AddRule(ctx, r.ZoneID, rs.ID, want)
	case sameRule(*have, want):
		res.rulesetID, res.ruleID = rs.ID, have.ID
		return res, nil
	default:
		want.ID = have.ID
		after, err = cl.UpdateRule(ctx, r.ZoneID, rs.ID, want)
	}
	if err != nil {
		return redirectResult{}, err
	}
	res.rulesetID = after.ID
	if got := c.findRule(&after, r); got != nil {
		res.ruleID = got.ID
	}
	return res, nil
}

// unredirect deletes what Dock made for r: its rule, then its
// placeholder record. What is gone already is no error.
func (c *Core) unredirect(ctx context.Context, cl *cloudflare.Client, r store.CloudflareRedirect) error {
	rs, err := cl.EntrypointRuleset(ctx, r.ZoneID, cloudflare.RedirectPhase)
	if err != nil {
		return err
	}
	if have := c.findRule(rs, r); have != nil {
		if err := cl.DeleteRule(ctx, r.ZoneID, rs.ID, have.ID); err != nil {
			return err
		}
	}
	existing, err := cl.RecordsNamed(ctx, r.ZoneID, r.Hostname)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.Comment == c.recordComment() && e.Type == "AAAA" && e.Content == redirectPlaceholder {
			if err := cl.DeleteRecord(ctx, r.ZoneID, e.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// redirectRule is the rule Dock wants for r: matching its hostname, to
// its target as it is or with the request's path and query after it.
// Hostnames and targets are checked to hold no quote or backslash, so
// they go into the expressions as they are.
func (c *Core) redirectRule(r store.CloudflareRedirect) cloudflare.Rule {
	to := cloudflare.RedirectTarget{StatusCode: int(r.StatusCode)}
	if r.KeepPath {
		to.TargetURL.Expression = fmt.Sprintf(`concat("%s", http.request.uri.path)`, strings.TrimSuffix(r.Target, "/"))
		to.PreserveQueryString = true
	} else {
		to.TargetURL.Value = r.Target
	}
	return cloudflare.Rule{
		Description:      c.ruleDescription(r.Hostname),
		Expression:       fmt.Sprintf(`(http.host eq "%s")`, r.Hostname),
		Action:           "redirect",
		ActionParameters: &cloudflare.RuleParameters{FromValue: &to},
		Enabled:          ptr(true),
	}
}

// ruleDescription marks the rule this control plane made for a hostname.
func (c *Core) ruleDescription(host string) string {
	return c.recordComment() + " redirect " + host
}

// findRule is Dock's rule for r in rs (nil for none): the one with its
// saved ID, or after a step that made it but didn't save, the one with
// its description.
func (c *Core) findRule(rs *cloudflare.Ruleset, r store.CloudflareRedirect) *cloudflare.Rule {
	if rs == nil {
		return nil
	}
	desc := c.ruleDescription(r.Hostname)
	for i, rule := range rs.Rules {
		if r.RuleID != "" && rule.ID == r.RuleID {
			return &rs.Rules[i]
		}
	}
	for i, rule := range rs.Rules {
		if rule.Description == desc {
			return &rs.Rules[i]
		}
	}
	return nil
}

// sameRule reports whether have already does what want does.
func sameRule(have, want cloudflare.Rule) bool {
	if have.Expression != want.Expression || have.Action != want.Action || have.Description != want.Description ||
		(have.Enabled != nil && !*have.Enabled) || have.ActionParameters == nil || have.ActionParameters.FromValue == nil {
		return false
	}
	return *have.ActionParameters.FromValue == *want.ActionParameters.FromValue
}

// dropRedirects deletes a connection's redirects in Cloudflare before
// the connection is removed with them (RemoveCloudflare's force; the
// rows are absent already). It returns the hostnames whose rule and
// record stay in Cloudflare: the token was refused, or doesn't reach
// their zone. Cloudflare not answering stops it, and the connection
// stays: the worker goes on removing the redirects.
func (c *Core) dropRedirects(ctx context.Context, conn store.CloudflareConnection, rows []store.CloudflareRedirect) ([]string, error) {
	c.cf.redirectsMu.Lock()
	defer c.cf.redirectsMu.Unlock()
	var left []string
	var cl *cloudflare.Client
	if conn.Status == string(apitypes.CloudflareOK) {
		var err error
		if cl, err = c.cfClient(conn); err != nil {
			return nil, err
		}
	}
	for _, r := range rows {
		if cl == nil || !c.holdsZone(conn.ID, r.ZoneID) {
			left = append(left, r.Hostname)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, cfCallTimeout)
		err := c.unredirect(cctx, cl, r)
		cancel()
		if e, ok := cloudflare.AsError(err); ok && e.Temporary() || err != nil && !ok {
			return nil, errf(apitypes.CodeConflict, "could not delete redirect %s in Cloudflare (%v); the connection stays and its redirects are being removed, try again in a moment", r.Hostname, err)
		}
		if err != nil {
			left = append(left, r.Hostname)
		}
	}
	return left, nil
}
