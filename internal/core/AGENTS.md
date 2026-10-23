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
- Org scoping (ADR 0027): look projects up within `a.OrgID`. A user or
  token must never see or name another org's projects, tokens, policy or
  audit events, including through live events (`canSee`).
- Errors are `datapanel.Error` with an `apitypes` code; the API maps codes
  to statuses. Messages are for humans and say what to do next.
- Secret values (variables, org variables, connection URLs) are sealed
  with `c.box` before they reach the store.
- Background goroutines are counted in `c.wg` so `Close` waits for them.
  A new ticker or timer needs a doc comment saying why and a row in
  docs/performance.md.
- Deploys: one per service at a time (`startRelease`, backed by a unique
  index), persisted as a state machine so recovery can fail it cleanly
  after a crash. Read `releases.go` and `deploy.go` before changing the
  flow.

## Tests

Use the harness in `core_test.go`: `newHarness(t)` gives a Core on the
fake runtime and a temp SQLite with org 1, `testSystem` acts in it, and
`restart` / `crash` simulate daemon restarts. Test behaviour through
exported methods, including the permission denials for each scope.
