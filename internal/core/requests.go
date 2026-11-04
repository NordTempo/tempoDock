package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Access requests (ADR 0044). A token's change that a locked environment
// refuses (checkEnv's LockedError) is kept as a request: the API and MCP
// layers, which know the call, hand it to FileRequest. An admin in the UI
// approves or rejects it; approving runs the same call again, as the token
// and through every check it would pass now, with the lock lifted for
// that one call. Core knows nothing of HTTP or MCP: each layer registers
// a Replayer that runs its own calls.

// Replayer runs a stored call again as a: the API's operation or MCP's
// tool op with its stored input args. It returns what the call returned,
// as JSON, for the requester.
type Replayer func(ctx context.Context, a Actor, op string, args json.RawMessage) (json.RawMessage, error)

// Call is a refused call as its layer knows it, to file as a request.
type Call struct {
	// Via names the Replayer ("api", "mcp"); Op the operation or tool,
	// Title its name for people ("Set variables").
	Via, Op, Title string
	// Args is the call's input, exactly as the Replayer takes it back.
	Args json.RawMessage
	// Reason is why the caller wants it, if it said.
	Reason string
}

// replayers holds the layers' Replayers, set once as they start.
type replayers struct {
	mu sync.RWMutex
	by map[string]Replayer
}

// SetReplayer registers how calls filed with Call.Via == via run again.
func (c *Core) SetReplayer(via string, r Replayer) {
	c.replays.mu.Lock()
	defer c.replays.mu.Unlock()
	if c.replays.by == nil {
		c.replays.by = map[string]Replayer{}
	}
	c.replays.by[via] = r
}

func (c *Core) replayer(via string) Replayer {
	c.replays.mu.RLock()
	defer c.replays.mu.RUnlock()
	return c.replays.by[via]
}

// requestRunWait is how long approving waits for the run, so a quick one
// answers with its result; a longer one carries on in the background.
const requestRunWait = 5 * time.Second

// defaultRequestTTL is how long a request waits when its org says nothing.
const defaultRequestTTL = 24 * time.Hour

// sealedRequestArgs binds a request's arguments to its row.
var sealedRequestArgs = secrets.Column{Table: "access_requests", Name: "args", Key: []string{"nonce"}}

// FileRequest turns err, when it is a token's LockedError, into the same
// error naming an access request for the call: a new one, or the pending
// one an identical call filed. Any other error is returned as it is.
func (c *Core) FileRequest(ctx context.Context, a Actor, err error, call Call) error {
	le, ok := errors.AsType[*LockedError](err)
	if !ok || le.Request != 0 || a.Kind != "token" || a.TokenID == 0 {
		return err
	}
	r, ferr := c.fileRequest(ctx, a, le, call)
	if ferr != nil {
		c.log.Error("file access request", "err", ferr)
		return err
	}
	return le.withRequest(r.ID)
}

// fileRequest stores a request for call, or finds the identical pending one.
func (c *Core) fileRequest(ctx context.Context, a Actor, le *LockedError, call Call) (store.AccessRequest, error) {
	args, err := canonicalJSON(call.Args)
	if err != nil {
		return store.AccessRequest{}, err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s\x00%s\x00%d\x00%s", a.TokenID, call.Via, call.Op, le.EnvID, args))
	fingerprint := hex.EncodeToString(sum[:])
	if r, err := c.store.GetPendingAccessRequest(ctx, fingerprint); err == nil && r.ExpiresAt.After(now()) {
		if call.Reason != "" && r.Reason == "" {
			if err := c.store.SetAccessRequestReason(ctx, store.SetAccessRequestReasonParams{Reason: call.Reason, ID: r.ID}); err != nil {
				return r, err
			}
			r.Reason = call.Reason
			c.publishRequest(r, le.Project, le.Environment)
		}
		return r, nil
	}
	org, err := c.store.GetOrg(ctx, le.OrgID)
	if err != nil {
		return store.AccessRequest{}, err
	}
	mark, err := c.store.LatestEnvironmentAudit(ctx, &le.EnvID)
	if err != nil {
		return store.AccessRequest{}, err
	}
	nonce := randomHex(16)
	sealed, err := c.box.Seal(args, sealedRequestArgs.AAD(nonce))
	if err != nil {
		return store.AccessRequest{}, err
	}
	t := now()
	var r store.AccessRequest
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		// An expired twin still holds the pending index until a retention
		// pass marks it; mark them now.
		if _, err := q.ExpireAccessRequests(ctx, t); err != nil {
			return err
		}
		r, err = q.CreateAccessRequest(ctx, store.CreateAccessRequestParams{
			Nonce: nonce, OrgID: le.OrgID, ProjectID: le.ProjectID, EnvironmentID: le.EnvID,
			Via: call.Via, Operation: call.Op, Title: call.Title, Action: le.Action, Args: sealed,
			Fingerprint: fingerprint, Reason: call.Reason, TokenID: &a.TokenID, RequestedBy: a.Name,
			AuditMark: mark, CreatedAt: t, ExpiresAt: t.Add(requestTTL(org)),
		})
		return err
	})
	if store.IsUnique(err) { // an identical call won the race
		return c.store.GetPendingAccessRequest(ctx, fingerprint)
	}
	if err != nil {
		return r, err
	}
	p := store.Project{ID: le.ProjectID, OrgID: le.OrgID, Name: le.Project}
	e := store.Environment{ID: le.EnvID, Name: le.Environment}
	c.audit(ctx, a, "request.create", &p, &e, fmt.Sprintf("request #%d", r.ID), requestSummary(r, le.Project, le.Environment)+reasonSuffix(r.Reason))
	c.publishRequest(r, le.Project, le.Environment)
	c.signalAlert(ctx, alertSignal{
		Key: requestAlertKey(r.ID), Kind: apitypes.AlertAccessRequest, Severity: apitypes.SeverityWarning,
		Title:   fmt.Sprintf("Access request #%d: %s", r.ID, requestSummary(r, le.Project, le.Environment)),
		Message: "Approve or reject it in Dock, under Requests." + reasonSuffix(r.Reason),
		Project: le.Project, Environment: le.Environment, projectID: le.ProjectID,
	})
	return r, nil
}

// requestTTL is how long the org's requests wait for a decision.
func requestTTL(o store.Org) time.Duration {
	if o.RequestTtlHours <= 0 {
		return defaultRequestTTL
	}
	return time.Duration(o.RequestTtlHours) * time.Hour
}

func requestAlertKey(id int64) string { return "request:" + strconv.FormatInt(id, 10) }

// requestSummary is a request in one line: "token claude: setting
// variables in shop/production".
func requestSummary(r store.AccessRequest, project, env string) string {
	return fmt.Sprintf("token %s: %s in %s/%s", r.RequestedBy, r.Action, project, env)
}

// canonicalJSON re-encodes JSON so equal values have equal bytes (object
// keys sorted), for fingerprints.
func canonicalJSON(b json.RawMessage) ([]byte, error) {
	if len(b) == 0 {
		return []byte("null"), nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// requestScope is a request's project and environment.
func (c *Core) requestScope(ctx context.Context, r store.AccessRequest) (envScope, error) {
	p, err := c.store.GetProject(ctx, r.ProjectID)
	if err != nil {
		return envScope{}, err
	}
	e, err := c.store.GetEnvironment(ctx, r.EnvironmentID)
	return envScope{p, e}, err
}

// toRequest is a request as the API shows it. A pending request past its
// time shows as expired before a retention pass marks it. Arguments are
// for admins only.
func (c *Core) toRequest(r store.AccessRequest, es envScope, args bool) apitypes.AccessRequest {
	out := apitypes.AccessRequest{
		ID: r.ID, Status: apitypes.AccessRequestStatus(r.Status), Project: es.project.Name, Environment: es.env.Name,
		Via: r.Via, Operation: r.Operation, Title: r.Title, Summary: requestSummary(r, es.project.Name, es.env.Name),
		Reason: r.Reason, RequestedBy: r.RequestedBy, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt, Comment: r.Comment, FinishedAt: r.FinishedAt, Error: r.Error,
	}
	if out.Status == apitypes.RequestPending && !r.ExpiresAt.After(now()) {
		out.Status, out.FinishedAt = apitypes.RequestExpired, &r.ExpiresAt
	}
	if r.Result != "" {
		out.Result = json.RawMessage(r.Result)
	}
	if args {
		if b, err := c.box.Open(r.Args, sealedRequestArgs.AAD(r.Nonce)); err == nil {
			out.Args = b
		} else {
			c.log.Error("open access request arguments", "request", r.ID, "err", err)
		}
	}
	return out
}

// publishRequest tells the org's admins and the token that asked.
func (c *Core) publishRequest(r store.AccessRequest, project, env string) {
	view := c.toRequest(r, envScope{store.Project{Name: project}, store.Environment{Name: env}}, false)
	c.bus.publish(busEvent{
		orgID: r.OrgID, projectID: r.ProjectID, envID: r.EnvironmentID, requester: derefOr(r.TokenID), adminsOnly: true,
		ev: apitypes.Event{Type: apitypes.EventAccessRequest, Project: project, Environment: env, AccessRequest: &view},
	})
}

// seesRequest reports whether the actor may see a request: the org's
// admins who can see its environment, and the token that asked.
func (a Actor) seesRequest(r store.AccessRequest) bool {
	if a.Kind == "token" && r.TokenID != nil && a.TokenID == *r.TokenID {
		return true
	}
	return rank(a.Scope) >= rank(apitypes.ScopeAdmin) && a.canSee(r.OrgID, r.ProjectID, r.EnvironmentID)
}

// RequestFilter narrows ListRequests.
type RequestFilter struct {
	// Status keeps one status; "" keeps all.
	Status apitypes.AccessRequestStatus
	Limit  int
}

// ListRequests returns the requests the actor may see, newest first: an
// admin's org's, or a token's own.
func (c *Core) ListRequests(ctx context.Context, a Actor, f RequestFilter) ([]apitypes.AccessRequest, error) {
	if err := a.require(apitypes.ScopeRead, "listing access requests"); err != nil {
		return nil, err
	}
	limit := int64(min(max(f.Limit, 1), 200))
	var rows []store.AccessRequest
	var err error
	switch {
	case a.Kind == "token" && rank(a.Scope) < rank(apitypes.ScopeAdmin):
		rows, err = c.store.ListTokenAccessRequests(ctx, store.ListTokenAccessRequestsParams{TokenID: &a.TokenID, Limit: limit})
	case rank(a.Scope) >= rank(apitypes.ScopeAdmin):
		rows, err = c.store.ListOrgAccessRequests(ctx, store.ListOrgAccessRequestsParams{OrgID: a.OrgID, Limit: limit})
	default:
		return nil, errf(apitypes.CodeForbidden, "%s has scope %s; access requests are for admins", a, a.Scope)
	}
	if err != nil {
		return nil, err
	}
	scopes, err := c.requestScopes(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := []apitypes.AccessRequest{}
	for _, r := range rows {
		if !a.seesRequest(r) {
			continue
		}
		v := c.toRequest(r, scopes[r.EnvironmentID], false)
		if f.Status == "" || v.Status == f.Status {
			out = append(out, v)
		}
	}
	return out, nil
}

// requestScopes loads the projects and environments of rows: two queries
// per distinct project, a handful per page.
func (c *Core) requestScopes(ctx context.Context, rows []store.AccessRequest) (map[int64]envScope, error) {
	out := map[int64]envScope{}
	projects := map[int64]bool{}
	for _, r := range rows {
		if projects[r.ProjectID] {
			continue
		}
		projects[r.ProjectID] = true
		p, err := c.store.GetProject(ctx, r.ProjectID)
		if err != nil {
			return nil, err
		}
		envs, err := c.store.ListEnvironments(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, e := range envs {
			out[e.ID] = envScope{p, e}
		}
	}
	return out, nil
}

// loadRequest loads a request the actor may see.
func (c *Core) loadRequest(ctx context.Context, a Actor, id int64) (store.AccessRequest, envScope, error) {
	r, err := c.store.GetAccessRequest(ctx, id)
	if store.IsNotFound(err) || (err == nil && !a.seesRequest(r) && a.Kind != "system") {
		return r, envScope{}, notFound("access request #%d not found; see `dock request list`", id)
	}
	if err != nil {
		return r, envScope{}, err
	}
	es, err := c.requestScope(ctx, r)
	return r, es, err
}

// GetRequest returns one request; admins get its arguments and how many
// changes its environment had since it was asked.
func (c *Core) GetRequest(ctx context.Context, a Actor, id int64) (apitypes.AccessRequest, error) {
	if err := a.require(apitypes.ScopeRead, "reading an access request"); err != nil {
		return apitypes.AccessRequest{}, err
	}
	r, es, err := c.loadRequest(ctx, a, id)
	if err != nil {
		return apitypes.AccessRequest{}, err
	}
	admin := rank(a.Scope) >= rank(apitypes.ScopeAdmin)
	out := c.toRequest(r, es, admin)
	if admin {
		n, err := c.store.CountEnvironmentAuditSince(ctx, store.CountEnvironmentAuditSinceParams{EnvironmentID: &r.EnvironmentID, ID: r.AuditMark})
		if err != nil {
			return out, err
		}
		changes := int(n)
		out.ChangesSince = &changes
	}
	return out, nil
}

// WaitRequest returns a request once it is decided and done (or failed),
// or as it is after wait.
func (c *Core) WaitRequest(ctx context.Context, a Actor, id int64, wait time.Duration) (apitypes.AccessRequest, error) {
	if err := a.require(apitypes.ScopeRead, "waiting for an access request"); err != nil {
		return apitypes.AccessRequest{}, err
	}
	if _, _, err := c.loadRequest(ctx, a, id); err != nil {
		return apitypes.AccessRequest{}, err
	}
	sub := c.bus.subscribe(func(e busEvent) bool {
		return e.ev.Type == apitypes.EventAccessRequest && e.ev.AccessRequest != nil && e.ev.AccessRequest.ID == id
	})
	defer sub.close()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		r, err := c.GetRequest(ctx, a, id)
		if err != nil || r.Status.Decided() {
			return r, err
		}
		if !r.ExpiresAt.After(now()) {
			return r, nil
		}
		expiry := time.NewTimer(time.Until(r.ExpiresAt))
		select {
		case <-sub.ch:
		case <-expiry.C:
		case <-timer.C:
			expiry.Stop()
			return r, nil
		case <-ctx.Done():
			expiry.Stop()
			return r, ctx.Err()
		case <-c.ctx.Done():
			expiry.Stop()
			return r, nil
		}
		expiry.Stop()
	}
}

// DecideRequest approves or rejects a pending request. Only people in a
// UI session decide, admins of its environment: a token never approves,
// not even its own request. Approving runs the call (runRequest) and
// waits a little for it, so a quick one answers with its outcome.
func (c *Core) DecideRequest(ctx context.Context, a Actor, id int64, approve bool, req apitypes.DecideAccessRequest) (apitypes.AccessRequest, error) {
	verb := "rejecting"
	if approve {
		verb = "approving"
	}
	if a.Kind == "token" {
		return apitypes.AccessRequest{}, errf(apitypes.CodeForbidden, "%s can't decide access requests: an admin decides them in the UI, under Requests", a)
	}
	if err := a.require(apitypes.ScopeAdmin, verb+" access requests"); err != nil {
		return apitypes.AccessRequest{}, err
	}
	r, es, err := c.loadRequest(ctx, a, id)
	if err != nil {
		return apitypes.AccessRequest{}, err
	}
	if err := a.checkEnvAccess(apitypes.ScopeAdmin, verb+" access requests", es.project, es.env); err != nil {
		return apitypes.AccessRequest{}, err
	}
	status := apitypes.RequestRejected
	if approve {
		status = apitypes.RequestApproved
	}
	t := now()
	decided, err := c.store.DecideAccessRequest(ctx, store.DecideAccessRequestParams{
		Status: string(status), DecidedBy: a.Name, DecidedAt: &t, Comment: req.Comment, ID: r.ID, ExpiresAt: t,
	})
	if store.IsNotFound(err) {
		cur := c.toRequest(r, es, false)
		return cur, conflict("access request #%d is %s already; nothing to decide", id, cur.Status)
	}
	if err != nil {
		return apitypes.AccessRequest{}, err
	}
	what := "rejected"
	if approve {
		what = "approved"
	}
	c.audit(ctx, a, "request."+map[bool]string{true: "approve", false: "reject"}[approve], &es.project, &es.env,
		fmt.Sprintf("request #%d", r.ID), fmt.Sprintf("%s %s%s", what, requestSummary(r, es.project.Name, es.env.Name), commentSuffix(req.Comment)))
	c.signalAlert(ctx, alertSignal{Key: requestAlertKey(r.ID), Resolve: true})
	c.publishRequest(decided, es.project.Name, es.env.Name)
	if !approve {
		return c.GetRequest(ctx, a, id)
	}
	done := make(chan struct{})
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(done)
		c.runRequest(decided, es, a)
	}()
	select {
	case <-done:
	case <-time.After(requestRunWait):
	case <-ctx.Done():
	}
	return c.GetRequest(ctx, a, id)
}

func commentSuffix(comment string) string {
	if comment == "" {
		return ""
	}
	return ": " + comment
}

// runRequest runs an approved request's call as the token that asked,
// as the token is now, with the lock lifted for this call alone, and
// records how it went.
func (c *Core) runRequest(r store.AccessRequest, es envScope, by Actor) {
	ctx := c.ctx
	result, err := c.replayRequest(ctx, r, es, by)
	status, msg := apitypes.RequestDone, ""
	if err != nil {
		status, msg = apitypes.RequestFailed, errorLine(err)
		if CodeOf(err) == apitypes.CodeUnauthorized {
			msg = "the token that asked is revoked, expired or no longer acts for its user; nothing ran"
		}
	}
	finished, ferr := c.store.FinishAccessRequest(context.WithoutCancel(ctx), store.FinishAccessRequestParams{
		Status: string(status), Result: string(result), Error: msg, FinishedAt: ptr(now()), ID: r.ID,
	})
	if ferr != nil {
		c.log.Error("finish access request", "request", r.ID, "err", ferr)
		return
	}
	c.publishRequest(finished, es.project.Name, es.env.Name)
}

// replayRequest rebuilds the requester and runs the call through its layer.
func (c *Core) replayRequest(ctx context.Context, r store.AccessRequest, es envScope, by Actor) (json.RawMessage, error) {
	run := c.replayer(r.Via)
	if run == nil {
		return nil, fmt.Errorf("no way to run %s calls", r.Via)
	}
	args, err := c.box.Open(r.Args, sealedRequestArgs.AAD(r.Nonce))
	if err != nil {
		return nil, err
	}
	org, err := c.store.GetOrg(ctx, r.OrgID)
	if err != nil {
		return nil, err
	}
	if r.TokenID == nil {
		return nil, fmt.Errorf("access request #%d has no token to run as", r.ID)
	}
	row, err := c.store.GetTokenByIDWithOwner(ctx, *r.TokenID)
	if err != nil {
		return nil, err
	}
	a, err := c.tokenActor(ctx, row, org.Slug)
	if err != nil {
		return nil, err
	}
	a.granted, a.approval = es.env.ID, &approval{id: r.ID, by: by.Name}
	return run(ctx, a, r.Operation, args)
}

// recoverRequests fails runs a restart cut off (nothing knows whether
// they finished) and expires what waited past its time; Start calls it.
func (c *Core) recoverRequests(ctx context.Context) error {
	rows, err := c.store.FailRunningAccessRequests(ctx, store.FailRunningAccessRequestsParams{
		Error: "the daemon restarted while it ran; check the environment, and ask again if it didn't happen", FinishedAt: ptr(now()),
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		c.log.Warn("access request interrupted by a restart", "request", r.ID)
	}
	return c.expireRequests(ctx, now())
}

// expireRequests marks requests past their time expired and resolves
// their alerts; the retention pass calls it.
func (c *Core) expireRequests(ctx context.Context, t time.Time) error {
	rows, err := c.store.ExpireAccessRequests(ctx, t)
	if err != nil {
		return err
	}
	scopes, err := c.requestScopes(ctx, rows)
	if err != nil {
		return err
	}
	for _, r := range rows {
		c.signalAlert(ctx, alertSignal{Key: requestAlertKey(r.ID), Resolve: true})
		es := scopes[r.EnvironmentID]
		c.publishRequest(r, es.project.Name, es.env.Name)
	}
	return nil
}

// derefOr is *p, or 0 for nil.
func derefOr(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
