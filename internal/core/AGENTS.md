# internal/core

Dock's domain logic. It reads and writes through `internal/store`, reaches
servers only through `node.Runtime` (`runtimeFor`), and knows nothing about
HTTP or Docker. The API, the MCP endpoint and admin commands are thin
callers of the exported methods here.

## Shape of an operation

```go
func (c *Core) DoThing(ctx context.Context, a Actor, req apitypes.DoThingRequest) (apitypes.Thing, error) {
	// 1. permission first: a.require / requireUnlimited / requireOperator,
	//    or checkProject / checkEnv once the project is loaded
	// 2. validate: invalid(...), notFound(...), conflict(...) (errors.go)
	// 3. write (c.store.Tx when it's more than one statement)
	// 4. c.audit(ctx, a, "thing.verb", p, e, target, summary): records
	//    and publishes the audit event
	// 5. publish any other live event open pages patch from (bus.go)
	// 6. return apitypes values, never store rows
}
```

- Every exported operation takes the `Actor` and checks it itself. Never
  rely on the API to have checked.
- A check's scope and action name a permission (ADR 0080): add the pair
  to `actionPerms` (shadow.go) with the check, or people are refused it.
  Write the action as a literal or a constant (a loader may pass it on);
  `TestEveryCheckHasAPermission` follows them to every call site and
  fails on one it can't read or that has no permission.
- Locked environments (ADR 0044): `checkEnv` refuses tokens any change
  (scope above read) in a locked environment. That one check is the lock;
  don't add per-operation lock code. So an operation that changes
  anything in an environment must load it through `envFor` /
  `serviceFor` (or call `checkEnv`) with the scope it really needs,
  never read scope plus a separate check. If it can't (an operator action
  that loads the service with read), call `a.checkLock`. A project-wide
  change that touches every environment calls `checkProjectLocks`.
  `internal/api`'s `TestLockedEnvironmentRefusesTokens` walks every
  mutating API operation under an environment and fails for one that
  skips the lock.
- Org scoping (ADR 0027): look projects up within `a.OrgID`. A user or
  token must never see or name another org's projects, tokens, policy or
  audit events, including through live events (`canSee`). Operators
  administer every org without membership (ADR 0062); an operator's
  action on an org is recorded in both logs with `c.auditBoth`.
- Sensitive actions (revealing secrets, creating tokens, unlocking an
  environment, managing accounts, 2FA and SSO settings) also call
  `a.requireRecent`: the session must have re-authenticated within
  `ReauthWindow` (ADR 0057).
- Errors are `datapanel.Error` with an `apitypes` code; the API maps codes
  to statuses. Messages are for humans and say what to do next. A
  permission refusal comes from the `Actor` checks (`refuse*` in
  refusal.go), which attach an `apitypes.Refusal`: why, what would allow
  it, and (through `Explain`) whom to ask (ADR 0080). Don't hand-write a
  forbidden error for what a scope, limit or operator check says.
- Anything that can show a service's variables (logs, build output, run
  output, a diagnosis's evidence) is masked for callers who can't reveal
  secrets (`logRedactor`, live streams with `redactor.writer`), and text
  every reader sees (a run's likely cause, events) with
  `releaseRedactor` or `newRedactor`. A scope that runs code (deploy) is
  not a scope that may read secrets.
- Secret values (variables, org variables, connection URLs) are sealed
  with `c.box` before they reach the store, bound to their column and
  row: a new sealed column goes in `sealedColumns` (sealed.go, ADR 0041).
- Background goroutines are counted in `c.wg` so `Close` waits for them.
  A new ticker or timer needs a doc comment saying why and a row in
  docs/performance.md.
- Background work that holds a claim (`claimService`, `lockForRestore`)
  releases it before recording its outcome (`runBackup`, `takeClaimed`):
  whoever reads "done" (the CLI's wait, MCP, a script) may start the next
  operation at once and must not be refused as busy.
- Never write to the store while holding a mutex other paths take
  (`alerts.mu` and the like): every one of them then waits behind
  SQLite's write lock and its busy wait, and under `synctest` the bubble
  hangs. Claim the thing first (a per-key hold, like `alertEngine.hold` or
  `holdDomainCheck`), read under the mutex, write without it, then store
  the result under it again. Tests wait on a barrier (`alertsHandled`),
  never a sleep.
- Deploys: one per service at a time (`startRelease`, backed by a unique
  index), persisted as a state machine so recovery can fail it cleanly
  after a crash. Read `releases.go` and `deploy.go` before changing the
  flow.

## Tests

Use the harness in `core_test.go`: `newHarness(t)` gives a Core on the
fake runtime and a temp SQLite with org 1, `testSystem` acts in it, and
`restart` / `crash` simulate daemon restarts. Test behaviour through
exported methods, including the permission denials for each scope.
