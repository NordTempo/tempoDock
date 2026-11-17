package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
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
	// Key, when set, makes one pending request per Key, and a newer call
	// replaces the call waiting in it (a newer push or upload of a
	// service); otherwise identical calls are one request.
	Key string
	// Upload is a held source archive in the requests directory (holdSource),
	// removed when the request ends.
	Upload string
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
	if !ok || le.Request != 0 || (a.Kind != "token" || a.TokenID == 0) && !le.person {
		return err
	}
	if call.Reason == "" {
		call.Reason = reasonOf(ctx)
	}
	r, ferr := c.fileRequest(ctx, a, le, call)
	if ferr != nil {
		c.log.Error("file access request", "err", ferr)
		return err
	}
	return le.withRequest(r.ID)
}

// personOf is the person a request is for when they asked themselves (a
// protected environment); nil for an agent's.
func personOf(a Actor, le *LockedError) *int64 {
	if le.person && a.UserID != 0 {
		return &a.UserID
	}
	return nil
}

// fileRequest stores a request for call as asked by a (a token, or for a
// held push the system acting for its pusher), or finds the pending one
// it joins. A newer call of the same Key ends the pending one and is filed
// as a new request.
func (c *Core) fileRequest(ctx context.Context, a Actor, le *LockedError, call Call) (store.AccessRequest, error) {
	args, err := canonicalJSON(call.Args)
	if err != nil {
		return store.AccessRequest{}, err
	}
	basis := call.Key
	if basis == "" {
		basis = string(args)
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\x00%d\x00%s\x00%s\x00%d\x00%s", a.TokenID, a.UserID, call.Via, call.Op, le.EnvID, basis))
	fingerprint := hex.EncodeToString(sum[:])
	org, err := c.store.GetOrg(ctx, le.OrgID)
	if err != nil {
		return store.AccessRequest{}, err
	}
	var tokenID *int64
	if a.TokenID != 0 {
		tokenID = &a.TokenID
	}
	nonce := randomHex(16)
	sealed, err := c.box.Seal(args, sealedRequestArgs.AAD(nonce))
	if err != nil {
		return store.AccessRequest{}, err
	}
	t := now()
	// replaced is the pending request a newer call of the same Key ends.
	// The newer call gets a new ID, so an approval of the one an admin
	// reviewed never runs content they did not see.
	var replaced *store.AccessRequest
	if r, err := c.store.GetPendingAccessRequest(ctx, fingerprint); err == nil && r.ExpiresAt.After(t) && call.Key != "" {
		replaced = &r
	} else if err == nil && r.ExpiresAt.After(t) {
		if call.Reason != "" && r.Reason == "" {
			if err := c.store.SetAccessRequestReason(ctx, store.SetAccessRequestReasonParams{Reason: call.Reason, ID: r.ID}); err != nil {
				return r, err
			}
			r.Reason = call.Reason
			c.publishRequest(r, le.Project, le.Environment, le.envKind)
		}
		return r, nil
	}
	mark, err := c.store.LatestEnvironmentAudit(ctx, &le.EnvID)
	if err != nil {
		return store.AccessRequest{}, err
	}
	var r store.AccessRequest
	var expired []store.AccessRequest
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		// An expired twin still holds the pending index until a retention
		// pass marks it; mark them now.
		if expired, err = q.ExpireAccessRequests(ctx, t); err != nil {
			return err
		}
		if replaced != nil {
			old, err := q.SupersedeAccessRequest(ctx, store.SupersedeAccessRequestParams{FinishedAt: &t, ID: replaced.ID})
			switch {
			case err == nil:
				expired = append(expired, old)
			case !errors.Is(err, sql.ErrNoRows): // decided meanwhile: just file the new one
				return err
			}
		}
		r, err = q.CreateAccessRequest(ctx, store.CreateAccessRequestParams{
			Nonce: nonce, OrgID: le.OrgID, ProjectID: le.ProjectID, EnvironmentID: le.EnvID,
			Via: call.Via, Operation: call.Op, Title: call.Title, Action: le.Action, Args: sealed,
			Fingerprint: fingerprint, Reason: call.Reason, TokenID: tokenID, RequestedBy: a.Name,
			AuditMark: mark, CreatedAt: t, ExpiresAt: t.Add(requestTTL(org)), Upload: call.Upload, AskUserID: le.askOwner, UserID: personOf(a, le),
		})
		return err
	})
	if store.IsUnique(err) { // an identical call won the race
		c.dropUpload(call.Upload)
		return c.store.GetPendingAccessRequest(ctx, fingerprint)
	}
	if err != nil {
		return r, err
	}
	c.endedRequests(ctx, expired)
	p := store.Project{ID: le.ProjectID, OrgID: le.OrgID, Name: le.Project}
	e := store.Environment{ID: le.EnvID, Name: le.Environment}
	if replaced != nil {
		c.audit(ctx, a, "request.replace", &p, &e, fmt.Sprintf("request #%d", r.ID), fmt.Sprintf("a newer call replaced request #%d: ", replaced.ID)+requestSummary(r, le.Project, le.Environment))
	} else {
		c.audit(ctx, a, "request.create", &p, &e, fmt.Sprintf("request #%d", r.ID), requestSummary(r, le.Project, le.Environment)+reasonSuffix(r.Reason))
	}
	c.publishRequest(r, le.Project, le.Environment, le.envKind)
	c.alertRequest(ctx, r, le)
	if replaced == nil {
		// A newer call replacing a pending one isn't mailed again.
		c.requestMail(ctx, r, le)
	}
	return r, nil
}

// alertRequest raises (or updates) a pending request's alert.
func (c *Core) alertRequest(ctx context.Context, r store.AccessRequest, le *LockedError) {
	c.signalAlert(ctx, alertSignal{
		Key: requestAlertKey(r.ID), Kind: apitypes.AlertAccessRequest, Severity: apitypes.SeverityWarning,
		Title:   fmt.Sprintf("Access request #%d: %s", r.ID, requestSummary(r, le.Project, le.Environment)),
		Message: "Approve or reject it in Dock, in the Inbox." + reasonSuffix(r.Reason),
		Command: fmt.Sprintf("dock request show %d", r.ID), Target: &apitypes.AlertTarget{Request: r.ID},
		Project: le.Project, Environment: le.Environment, projectID: le.ProjectID,
	})
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
// variables in shop/production", or for a held push "github:alice:
// deploying a push in shop/production".
func requestSummary(r store.AccessRequest, project, env string) string {
	who := r.RequestedBy
	if r.TokenID != nil {
		who = "token " + who
	}
	return fmt.Sprintf("%s: %s in %s/%s", who, r.Action, project, env)
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
func (c *Core) publishRequest(r store.AccessRequest, project, env string, kind uint8) {
	view := c.toRequest(r, envScope{store.Project{Name: project}, store.Environment{Name: env}}, false)
	c.bus.publish(busEvent{
		orgID: r.OrgID, projectID: r.ProjectID, envID: r.EnvironmentID, envKind: kind, requester: derefOr(r.TokenID), adminsOnly: true, asker: derefOr(r.AskUserID),
		ev: apitypes.Event{Type: apitypes.EventAccessRequest, Project: project, Environment: env, AccessRequest: &view},
	})
}

// seesRequest reports whether the actor may see a request in an
// environment of kind: whoever decides it (decides), and the token that
// asked.
func (a Actor) seesRequest(r store.AccessRequest, kind uint8) bool {
	if a.Kind == "token" && r.TokenID != nil && a.TokenID == *r.TokenID {
		return true
	}
	if a.Kind == "user" && r.UserID != nil && a.UserID == *r.UserID {
		return true // a person's own, in a protected environment
	}
	return a.decides(r, kind) && a.canSee(r.OrgID, r.ProjectID, r.EnvironmentID)
}

// decides reports whether the actor decides a request in an environment
// of kind: the person whose agent asked them first (ADR 0080, their agent
// setting), signed in themselves; otherwise whoever may approve there.
func (a Actor) decides(r store.AccessRequest, kind uint8) bool {
	if r.AskUserID != nil {
		return a.Kind != "token" && a.UserID == *r.AskUserID
	}
	return a.approves(r.ProjectID, r.EnvironmentID, kind)
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
	kinds, err := c.envKinds(ctx, a)
	if err != nil {
		return nil, err
	}
	// Both lists join the project's and environment's names: one query.
	type row struct {
		r            store.AccessRequest
		project, env string
	}
	var rows []row
	switch {
	case a.Kind == "token" && rank(a.Scope) < rank(apitypes.ScopeAdmin):
		got, err := c.store.ListTokenAccessRequests(ctx, store.ListTokenAccessRequestsParams{TokenID: &a.TokenID, Limit: limit})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			rows = append(rows, row{g.AccessRequest, g.ProjectName, g.EnvironmentName})
		}
	case a.approves(0, 0, 0):
		got, err := c.store.ListOrgAccessRequests(ctx, store.ListOrgAccessRequestsParams{OrgID: a.OrgID, Limit: limit})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			if !a.seesRequest(g.AccessRequest, kinds[g.AccessRequest.EnvironmentID]) {
				continue // an approver of other environments only
			}
			rows = append(rows, row{g.AccessRequest, g.ProjectName, g.EnvironmentName})
		}
	case a.Kind != "token" && a.UserID != 0:
		// What their agents asked them first, and what they asked.
		got, err := c.store.ListAskedAccessRequests(ctx, store.ListAskedAccessRequestsParams{UserID: &a.UserID, OrgID: a.OrgID, Limit: limit})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			rows = append(rows, row{g.AccessRequest, g.ProjectName, g.EnvironmentName})
		}
	default:
		return nil, errf(apitypes.CodeForbidden, "%s has scope %s; access requests are for admins", a, a.Scope)
	}
	out := []apitypes.AccessRequest{}
	for _, r := range rows {
		if !a.seesRequest(r.r, kinds[r.r.EnvironmentID]) {
			continue
		}
		v := c.toRequest(r.r, envScope{store.Project{Name: r.project}, store.Environment{Name: r.env}}, false)
		v.Decide = a.Kind != "token" && a.decides(r.r, kinds[r.r.EnvironmentID])
		if f.Status == "" || v.Status == f.Status {
			out = append(out, v)
		}
	}
	return out, nil
}

// requestScopes loads the projects and environments of rows: two queries
// per distinct project. Only endedRequests uses it, for the few requests
// an expiry or restart ends; listing joins the names instead.
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
	missing := notFound("access request #%d not found; see `dock request list`", id)
	r, err := c.store.GetAccessRequest(ctx, id)
	if store.IsNotFound(err) || err == nil && r.OrgID != a.OrgID && a.Kind != "system" {
		return r, envScope{}, missing
	}
	if err != nil {
		return r, envScope{}, err
	}
	// Who sees it depends on its environment's kind (approves).
	es, err := c.requestScope(ctx, r)
	if err != nil {
		return r, es, err
	}
	if !a.seesRequest(r, envKind(es.env)) && a.Kind != "system" {
		return r, envScope{}, missing
	}
	return r, es, nil
}

// GetRequest returns one request; whoever decides it gets its arguments
// and how many changes its environment had since it was asked. Deciding
// isn't revealing (ADR 0080: approvers approve without being admins): a
// variables request shows its values, the current ones and the asked
// ones, only to someone who may reveal values there; others see which
// keys change and how.
func (c *Core) GetRequest(ctx context.Context, a Actor, id int64) (apitypes.AccessRequest, error) {
	if err := a.require(apitypes.ScopeRead, "reading an access request"); err != nil {
		return apitypes.AccessRequest{}, err
	}
	r, es, err := c.loadRequest(ctx, a, id)
	if err != nil {
		return apitypes.AccessRequest{}, err
	}
	admin := a.decides(r, envKind(es.env))
	out := c.toRequest(r, es, admin)
	out.Decide = admin && a.Kind != "token"
	if admin {
		n, err := c.store.CountEnvironmentAuditSince(ctx, store.CountEnvironmentAuditSinceParams{EnvironmentID: &r.EnvironmentID, ID: r.AuditMark})
		if err != nil {
			return out, err
		}
		changes := int(n)
		out.ChangesSince = &changes
		reveal := a.revealsIn(es)
		out.Changes = c.requestPreview(ctx, r, out.Args, reveal)
		if !reveal {
			out.Args = withoutVariableValues(r.Via, r.Operation, out.Args)
		}
	}
	return out, nil
}

// GiveRequestReason gives the caller's own pending request a reason when it
// has none, as an identical call with one would: what the approver reads.
// A request that has a reason keeps it.
func (c *Core) GiveRequestReason(ctx context.Context, a Actor, id int64, reason string) error {
	if reason == "" {
		return nil // nothing to give: anyone who may see it may wait on it
	}
	r, es, err := c.loadRequest(ctx, a, id)
	if err != nil {
		return err
	}
	switch {
	case a.TokenID == 0 || derefOr(r.TokenID) != a.TokenID:
		return errf(apitypes.CodeForbidden, "only the token that filed access request #%d gives it a reason", id)
	case r.Status != string(apitypes.RequestPending) || r.Reason != "":
		return nil
	}
	if err := c.store.SetAccessRequestReason(ctx, store.SetAccessRequestReasonParams{Reason: reason, ID: r.ID}); err != nil {
		return err
	}
	r.Reason = reason
	c.publishRequest(r, es.project.Name, es.env.Name, envKind(es.env))
	return nil
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
		return apitypes.AccessRequest{}, errf(apitypes.CodeForbidden, "%s can't decide access requests: an admin decides them in the UI, in the Inbox", a)
	}
	// Approving lets a token change what it couldn't alone: a step-up
	// (ADR 0057), before anything about the request is told. Rejecting
	// only takes power away.
	if approve {
		if err := a.requireRecent("approving an access request"); err != nil {
			return apitypes.AccessRequest{}, err
		}
	}
	r, es, err := c.loadRequest(ctx, a, id)
	if err != nil {
		return apitypes.AccessRequest{}, err
	}
	if r.AskUserID == nil {
		if err := a.require(apitypes.ScopeAdmin, verb+" access requests"); err != nil {
			return apitypes.AccessRequest{}, err
		}
		if err := a.checkEnvAccess(apitypes.ScopeAdmin, verb+" access requests", es.project, es.env); err != nil {
			return apitypes.AccessRequest{}, err
		}
	} else if !a.decides(r, envKind(es.env)) {
		// loadRequest only finds it for its person and the asking agent.
		return apitypes.AccessRequest{}, errf(apitypes.CodeForbidden, "access request #%d waits for the person whose agent asked them first", id)
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
	c.publishRequest(decided, es.project.Name, es.env.Name, envKind(es.env))
	if !approve {
		c.dropUpload(decided.Upload)
		c.endHeldPush(ctx, decided, "the request was rejected")
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
	result, err := c.replayRequest(ctx, r, es, by, false)
	status, msg := apitypes.RequestDone, ""
	if err != nil {
		status, msg = apitypes.RequestFailed, c.errorLine(err)
		if CodeOf(err) == apitypes.CodeUnauthorized {
			msg = "the token that asked is revoked, expired or no longer acts for its user; nothing ran"
		}
	}
	finished, ferr := c.store.FinishAccessRequest(context.WithoutCancel(ctx), store.FinishAccessRequestParams{
		Status: string(status), Result: string(result), Error: msg, FinishedAt: ptr(now()), ID: r.ID,
	})
	c.dropUpload(r.Upload)
	if ferr != nil {
		c.log.Error("finish access request", "request", r.ID, "err", ferr)
		return
	}
	c.publishRequest(finished, es.project.Name, es.env.Name, envKind(es.env))
}

// restartOps are the calls that restart a service, by Via and Op: their
// one rule after the lock (an active release to copy) is checked too.
var restartOps = map[string]bool{"api:restart": true, "mcp:redeploy": true}

// CheckRequest says whether approving a pending request would run now:
// it replays the call as approving would, but as a check-only actor that
// every lock stops (lockedOut), so the call passes through its own
// checks (the token still valid and allowed, its project, environment
// and service still there) and stops before changing anything. Reaching
// the lock means it would run; a restart also needs an active release.
// Whoever may decide the request may check it; no step-up, since
// nothing runs.
func (c *Core) CheckRequest(ctx context.Context, a Actor, id int64) (apitypes.AccessRequestCheck, error) {
	if a.Kind == "token" {
		return apitypes.AccessRequestCheck{}, errf(apitypes.CodeForbidden, "%s can't decide access requests: an admin decides them in the UI, in the Inbox", a)
	}
	r, es, err := c.loadRequest(ctx, a, id)
	if err != nil {
		return apitypes.AccessRequestCheck{}, err
	}
	if r.AskUserID == nil {
		if err := a.require(apitypes.ScopeAdmin, "checking access requests"); err != nil {
			return apitypes.AccessRequestCheck{}, err
		}
		if err := a.checkEnvAccess(apitypes.ScopeAdmin, "checking access requests", es.project, es.env); err != nil {
			return apitypes.AccessRequestCheck{}, err
		}
	} else if !a.decides(r, envKind(es.env)) {
		return apitypes.AccessRequestCheck{}, errf(apitypes.CodeForbidden, "access request #%d waits for the person whose agent asked them first", id)
	}
	if st := c.toRequest(r, es, false).Status; st != apitypes.RequestPending {
		return apitypes.AccessRequestCheck{Reason: fmt.Sprintf("access request #%d is %s; only a pending one is approved", id, st)}, nil
	}
	_, err = c.replayRequest(ctx, r, es, a, true)
	le, locked := errors.AsType[*LockedError](err)
	switch {
	case locked && le.EnvID == es.env.ID:
	case CodeOf(err) == apitypes.CodeUnauthorized:
		return apitypes.AccessRequestCheck{Reason: "the token that asked is revoked, expired or no longer acts for its user"}, nil
	case err != nil:
		return apitypes.AccessRequestCheck{Reason: c.errorLine(err)}, nil
	default:
		// The call reached no lock, so it would change nothing here.
		return apitypes.AccessRequestCheck{Reason: "the call no longer changes anything in " + es.String()}, nil
	}
	if le.Service != "" && restartOps[r.Via+":"+r.Operation] {
		ss, err := c.loadService(ctx, a.OrgID, ServiceRef{es.project.Name, es.env.Name, le.Service})
		if err != nil {
			return apitypes.AccessRequestCheck{}, err
		}
		if _, err := c.restartable(ctx, ss); err != nil {
			return apitypes.AccessRequestCheck{Reason: c.errorLine(err), Service: le.Service}, nil
		}
	}
	return apitypes.AccessRequestCheck{OK: true}, nil
}

// replayRequest rebuilds the requester and runs the call through its
// layer: as approved (runRequest), or with check as CheckRequest's
// check-only actor.
func (c *Core) replayRequest(ctx context.Context, r store.AccessRequest, es envScope, by Actor, check bool) (json.RawMessage, error) {
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
	// A held push runs as the daemon, for its pusher.
	a := System.InOrg(r.OrgID)
	a.Name = r.RequestedBy
	switch {
	case r.UserID != nil:
		// A person's, in a protected environment: as them, now.
		u, err := c.store.GetUser(ctx, *r.UserID)
		if err != nil {
			return nil, err
		}
		if u.DisabledAt != nil {
			return nil, errf(apitypes.CodeForbidden, "%s's account is disabled; the request can't run", u.Email)
		}
		if a, err = c.actIn(ctx, userActor(u), org.Slug, apitypes.ScopeAdmin); err != nil {
			return nil, err
		}
	case r.TokenID != nil:
		row, err := c.store.GetTokenByIDWithOwner(ctx, *r.TokenID)
		if err != nil {
			return nil, err
		}
		if a, err = c.tokenActor(ctx, row, org.Slug); err != nil {
			return nil, err
		}
	}
	if check {
		a.checkOnly = true
	} else {
		a.granted, a.approval = es.env.ID, &approval{id: r.ID, by: by.Name}
	}
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
	c.endedRequests(ctx, rows)
	if err := c.sweepUploads(ctx); err != nil {
		c.log.Warn("sweep held uploads", "err", err)
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
	c.endedRequests(ctx, rows)
	return nil
}

// unlockedComment is why an unlocked environment's pending requests
// ended, as their comment.
const unlockedComment = "the environment was unlocked, so the call needs no approval now: call again"

// endUnlockedRequests expires the pending requests of an environment an
// admin unlocked or opened (UnlockEnvironment): waiters hear the call may
// run as it is, and their alerts resolve. Approving one later would run a
// call nobody needs to review any more.
func (c *Core) endUnlockedRequests(ctx context.Context, envID int64) {
	rows, err := c.store.EndUnlockedAccessRequests(ctx, store.EndUnlockedAccessRequestsParams{Now: ptr(now()), Comment: unlockedComment, EnvironmentID: envID})
	if err != nil {
		c.log.Error("end an unlocked environment's access requests", "environment", envID, "err", err)
		return
	}
	c.endedRequests(ctx, rows)
}

// endOrphanedRequests rejects, for the system, pending requests that can
// no longer run (their token revoked, their project archived), so they
// end at once instead of failing when approved. Revoking tokens and
// archiving a project call it after their change.
func (c *Core) endOrphanedRequests(ctx context.Context) {
	rows, err := c.store.EndOrphanedAccessRequests(ctx, ptr(now()))
	if err != nil {
		c.log.Error("end orphaned access requests", "err", err)
		return
	}
	c.endedRequests(ctx, rows)
}

// endedRequests finishes requests that ended without running (expired,
// cut off by a restart, orphaned, replaced by a newer call, their
// environment unlocked): their alerts
// resolve, their held archives go, an expired held push's commit status
// fails and open pages hear of it.
func (c *Core) endedRequests(ctx context.Context, rows []store.AccessRequest) {
	if len(rows) == 0 {
		return
	}
	scopes, err := c.requestScopes(ctx, rows)
	if err != nil {
		c.log.Error("ended access requests", "err", err)
		return
	}
	for _, r := range rows {
		c.signalAlert(ctx, alertSignal{Key: requestAlertKey(r.ID), Resolve: true})
		c.dropUpload(r.Upload)
		if r.Status == string(apitypes.RequestExpired) {
			c.endHeldPush(ctx, r, cmp.Or(r.Comment, "the request expired"))
		}
		es := scopes[r.EnvironmentID]
		c.publishRequest(r, es.project.Name, es.env.Name, envKind(es.env))
	}
}

// derefOr is *p, or 0 for nil.
func derefOr(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
