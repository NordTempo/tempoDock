package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Update hooks of status pages (ADR 0073, phase 5): Slack and Discord
// incoming webhooks and signed JSON webhooks a page's admins set, where
// Dock posts every public update of the page's incidents and maintenance
// windows. They go through the alert engine's delivery: its client (no
// redirects, never this server's or link-local addresses), its per-target
// queue and its retries. There is no public subscribe form and no mail:
// letting anyone register URLs would have Dock post wherever a stranger
// says. Visitors follow the Atom feed instead.

// hookConfig is a hook's sealed settings.
type hookConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

// hookDelivery is one post queued for a hook.
type hookDelivery struct {
	post apitypes.StatusHookPost
}

// statusHookAAD binds a hook's sealed settings to its page and name.
func statusHookAAD(page int64, name string) secrets.AAD {
	return sealedStatusHook.AAD(strconv.FormatInt(page, 10), name)
}

// toStatusHook is a hook as lists show it.
func toStatusHook(h store.StatusHook) apitypes.StatusHook {
	out := apitypes.StatusHook{Name: h.Name, Kind: apitypes.StatusHookKind(h.Kind), Target: h.Target, LastSentAt: h.LastSentAt,
		CreatedAt: h.CreatedAt, CreatedBy: h.CreatedBy}
	if h.LastErrorAt != nil && (h.LastSentAt == nil || h.LastErrorAt.After(*h.LastSentAt)) {
		out.LastError, out.LastErrorAt = h.LastError, h.LastErrorAt
	}
	return out
}

// ListStatusHooks lists a page's hooks, without their URLs' paths or
// secrets. Organization admins.
func (c *Core) ListStatusHooks(ctx context.Context, a Actor, slug string) ([]apitypes.StatusHook, error) {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "reading a status page's hooks")
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListStatusHooks(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.StatusHook, len(rows))
	for i, r := range rows {
		out[i] = toStatusHook(r)
	}
	return out, nil
}

// AddStatusHook adds a hook to a page. A webhook without a secret gets a
// generated one, returned only here. Organization admins.
func (c *Core) AddStatusHook(ctx context.Context, a Actor, slug string, req apitypes.AddStatusHookRequest) (apitypes.CreatedStatusHook, error) {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "adding a status page hook")
	if err != nil {
		return apitypes.CreatedStatusHook{}, err
	}
	if err := validName("hook", req.Name); err != nil {
		return apitypes.CreatedStatusHook{}, err
	}
	u, perr := url.Parse(req.URL)
	if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return apitypes.CreatedStatusHook{}, invalid("a hook needs an https:// URL: the %s incoming webhook's address", req.Kind)
	}
	// Named hosts are checked when posting (newChannelClient), after DNS.
	if err := c.alerts.orgGuard.hostErr(u.Hostname()); err != nil {
		return apitypes.CreatedStatusHook{}, invalid("%v", err)
	}
	cfg := hookConfig{URL: req.URL}
	var generated string
	switch req.Kind {
	case apitypes.HookWebhook:
		cfg.Secret = req.Secret
		if cfg.Secret == "" {
			b := make([]byte, 32)
			_, _ = rand.Read(b)
			cfg.Secret = hex.EncodeToString(b)
			generated = cfg.Secret
		}
	case apitypes.HookSlack, apitypes.HookDiscord:
		if req.Secret != "" {
			return apitypes.CreatedStatusHook{}, invalid("only webhook hooks are signed; %s hooks take no secret", req.Kind)
		}
	default:
		return apitypes.CreatedStatusHook{}, invalid("a hook's kind is slack, discord or webhook")
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return apitypes.CreatedStatusHook{}, err
	}
	sealed, err := c.box.Seal(plain, statusHookAAD(p.ID, req.Name))
	if err != nil {
		return apitypes.CreatedStatusHook{}, err
	}
	target := u.Scheme + "://" + u.Host
	row, err := c.store.CreateStatusHook(ctx, store.CreateStatusHookParams{PageID: p.ID, Name: req.Name, Kind: string(req.Kind),
		Target: target, Config: sealed, CreatedAt: now(), CreatedBy: a.Name})
	if store.IsUnique(err) {
		return apitypes.CreatedStatusHook{}, conflict("status page %s has a hook called %s; remove it first to change it", p.Slug, req.Name)
	}
	if err != nil {
		return apitypes.CreatedStatusHook{}, err
	}
	c.audit(ctx, a, "status_page.hook_add", nil, nil, "status page "+p.Slug, fmt.Sprintf("status page %s: added %s hook %s to %s", p.Slug, req.Kind, req.Name, target))
	return apitypes.CreatedStatusHook{Hook: toStatusHook(row), Secret: generated}, nil
}

// statusHook is a page's hook by name.
func (c *Core) statusHook(ctx context.Context, p store.StatusPage, name string) (store.StatusHook, error) {
	h, err := c.store.GetStatusHook(ctx, store.GetStatusHookParams{PageID: p.ID, Name: name})
	if store.IsNotFound(err) {
		return h, notFound("status page %s has no hook %q; `dock status-page hook list %s` lists them", p.Slug, name, p.Slug)
	}
	return h, err
}

// RemoveStatusHook removes a page's hook; posts it is retrying stop with
// the next attempt. Organization admins.
func (c *Core) RemoveStatusHook(ctx context.Context, a Actor, slug, name string) error {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "removing a status page hook")
	if err != nil {
		return err
	}
	h, err := c.statusHook(ctx, p, name)
	if err != nil {
		return err
	}
	if err := c.store.DeleteStatusHook(ctx, h.ID); err != nil {
		return err
	}
	c.audit(ctx, a, "status_page.hook_remove", nil, nil, "status page "+p.Slug, "status page "+p.Slug+": removed hook "+name)
	return nil
}

// TestStatusHook posts a test update to a page's hook once, without
// retries, and says how it went; the outcome is recorded on the hook.
// Organization admins.
func (c *Core) TestStatusHook(ctx context.Context, a Actor, slug, name string) (apitypes.StatusHookTest, error) {
	p, err := c.loadStatusPage(ctx, a, slug, apitypes.ScopeAdmin, "testing a status page hook")
	if err != nil {
		return apitypes.StatusHookTest{}, err
	}
	h, err := c.statusHook(ctx, p, name)
	if err != nil {
		return apitypes.StatusHookTest{}, err
	}
	t := now()
	post := apitypes.StatusHookPost{Event: "test", SentAt: t, Page: apitypes.StatusHookPage{Title: p.Title, URL: c.statusPageURL(ctx, p)},
		Incident: apitypes.StatusHookIncident{ID: "test", Kind: apitypes.KindIncident, Title: "Test from Dock", State: apitypes.IncidentResolved,
			Components: []apitypes.StatusImpact{}, StartsAt: t},
		Update: apitypes.StatusHookUpdateBody{State: apitypes.IncidentResolved, Message: "Hook " + name + " works: updates of " + p.Title + " come here.", At: t}}
	out := apitypes.StatusHookTest{OK: true}
	if err := c.sendHook(ctx, h, post); err != nil {
		out.OK, out.Error = false, err.Error()
		c.hookFailed(ctx, h.ID, err)
	} else {
		c.hookSent(ctx, h.ID)
	}
	if h, err = c.store.GetStatusHookByID(ctx, h.ID); err != nil {
		return out, err
	}
	out.Hook = toStatusHook(h)
	return out, nil
}

// statusUpdatePosted queues a public update for the page's hooks: an
// incident's or window's update u, as the page shows it. Nothing for an
// unpublished page. One indexed query per update posted.
func (c *Core) statusUpdatePosted(ctx context.Context, p store.StatusPage, inc store.StatusIncident, u store.StatusUpdate) {
	if !p.Published {
		return
	}
	hooks, err := c.store.ListStatusHooks(ctx, p.ID)
	if err != nil {
		c.log.Error("status pages: list hooks", "page", p.Slug, "err", err)
		return
	}
	if len(hooks) == 0 {
		return
	}
	full, err := c.loadIncident(ctx, inc)
	if err != nil {
		c.log.Error("status pages: read an incident for its hooks", "incident", inc.PublicID, "err", err)
		return
	}
	pageURL := c.statusPageURL(ctx, p)
	post := apitypes.StatusHookPost{Event: "update", SentAt: now(), Page: apitypes.StatusHookPage{Title: p.Title, URL: pageURL},
		Incident: apitypes.StatusHookIncident{ID: inc.PublicID, Kind: apitypes.IncidentKind(inc.Kind), Title: inc.Title, State: apitypes.IncidentState(u.State),
			Components: toImpacts(full.impacts), StartsAt: inc.StartsAt, EndsAt: inc.EndsAt},
		Update: apitypes.StatusHookUpdateBody{State: apitypes.IncidentState(u.State), Message: u.Body, At: u.At}}
	if pageURL != "" {
		post.Incident.URL = pageURL + "/incidents/" + inc.PublicID
	}
	for _, h := range hooks {
		c.queueHook(h.ID, post)
	}
}

// queueHook queues a post for a hook and starts its sender if it isn't
// running, through the alert engine's delivery queue.
func (c *Core) queueHook(id int64, post apitypes.StatusHookPost) {
	start, full := c.alerts.hooks.add(id, hookDelivery{post: post})
	if full {
		c.hookFailed(c.ctx, id, errors.New("too many updates waiting to be posted; this one was dropped"))
		return
	}
	if !start {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.alerts.hooks.sendAll(c.ctx, id, func(d hookDelivery) { c.postWithRetries(id, d.post) })
	}()
}

// postWithRetries posts an update to a hook with the alert channels'
// retries and records the outcome. A hook removed meanwhile isn't posted
// to.
func (c *Core) postWithRetries(id int64, post apitypes.StatusHookPost) {
	err := c.withRetries(func() error {
		h, err := c.store.GetStatusHookByID(c.ctx, id)
		if err != nil {
			return errGone
		}
		return c.sendHook(c.ctx, h, post)
	})
	switch {
	case err == nil:
		c.hookSent(c.ctx, id)
	case errors.Is(err, errGone) || c.ctx.Err() != nil:
	default:
		c.log.Warn("status pages: hook delivery failed", "hook", id, "err", err)
		c.hookFailed(c.ctx, id, err)
	}
}

// sendHook makes one attempt at posting to a hook, in its kind's format.
func (c *Core) sendHook(ctx context.Context, h store.StatusHook, post apitypes.StatusHookPost) error {
	plain, err := c.box.Open(h.Config, statusHookAAD(h.PageID, h.Name))
	if err != nil {
		return fmt.Errorf("open hook settings: %w", err)
	}
	var cfg hookConfig
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	switch apitypes.StatusHookKind(h.Kind) {
	case apitypes.HookWebhook:
		body, err := json.Marshal(post)
		if err != nil {
			return err
		}
		return c.alerts.post(ctx, true, cfg.URL, body, map[string]string{SignatureHeader: signWebhook(cfg.Secret, time.Now(), body), "Dock-Event": "status." + post.Event})
	case apitypes.HookSlack:
		body, err := json.Marshal(map[string]string{"text": hookText(post, true)})
		if err != nil {
			return err
		}
		return c.alerts.post(ctx, true, cfg.URL, body, nil)
	case apitypes.HookDiscord:
		// Discord takes at most 2000 characters, and mentions nobody.
		body, err := json.Marshal(map[string]any{"content": clip(hookText(post, false), 1990), "allowed_mentions": map[string][]string{"parse": {}}})
		if err != nil {
			return err
		}
		return c.alerts.post(ctx, true, cfg.URL, body, nil)
	}
	return fmt.Errorf("unknown hook kind %q", h.Kind)
}

// hookStates are the words for an update's state in a hook post.
var hookStates = map[apitypes.IncidentState]string{
	apitypes.IncidentInvestigating: "Investigating", apitypes.IncidentIdentified: "Identified", apitypes.IncidentMonitoring: "Monitoring",
	apitypes.IncidentResolved: "Resolved", apitypes.MaintenanceScheduled: "Scheduled", apitypes.MaintenanceInProgress: "In progress",
	apitypes.MaintenanceCompleted: "Completed", apitypes.MaintenanceCancelled: "Cancelled",
}

// hookText is a post as a chat message: the page and title, the state,
// the update's words and the link. slack escapes what Slack reads as
// markup.
func hookText(post apitypes.StatusHookPost, slack bool) string {
	esc := func(s string) string { return s }
	if slack {
		esc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	}
	var b strings.Builder
	what := "Incident"
	if post.Incident.Kind == apitypes.KindMaintenance {
		what = "Maintenance"
	}
	fmt.Fprintf(&b, "%s on %s: %s (%s)\n", what, esc(post.Page.Title), esc(post.Incident.Title), hookStates[post.Update.State])
	if post.Incident.Kind == apitypes.KindMaintenance && post.Incident.EndsAt != nil {
		fmt.Fprintf(&b, "%s to %s UTC\n", post.Incident.StartsAt.UTC().Format("Mon 2 Jan 15:04"), post.Incident.EndsAt.UTC().Format("15:04"))
	}
	if len(post.Incident.Components) > 0 {
		names := make([]string, len(post.Incident.Components))
		for i, im := range post.Incident.Components {
			names[i] = im.Component
		}
		b.WriteString("Affects " + esc(strings.Join(names, ", ")) + "\n")
	}
	b.WriteString(esc(post.Update.Message))
	if post.Incident.URL != "" {
		b.WriteString("\n" + post.Incident.URL)
	}
	return b.String()
}

func (c *Core) hookSent(ctx context.Context, id int64) {
	if err := c.store.SetStatusHookSent(ctx, store.SetStatusHookSentParams{LastSentAt: ptr(now()), ID: id}); err != nil {
		c.log.Error("status pages: record a hook delivery", "err", err)
	}
}

func (c *Core) hookFailed(ctx context.Context, id int64, cause error) {
	if err := c.store.SetStatusHookFailed(ctx, store.SetStatusHookFailedParams{LastError: clip(cause.Error(), 500), LastErrorAt: ptr(now()), ID: id}); err != nil {
		c.log.Error("status pages: record a hook delivery", "err", err)
	}
}
