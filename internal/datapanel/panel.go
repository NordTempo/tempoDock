package datapanel

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/secrets"
)

// Panel is the database panel for one host (ADR 0016, 0024). The host
// resolves each request to a Database (visibility and Decide) and calls
// the Panel's methods, which check the caller's level, talk to the
// database through the engine and keep their state through the Host.
// Every method that reaches the database audits what it did. It is safe
// for concurrent use; create it with New.
type Panel[D any] struct {
	host Host[D]
	// engines are Options.Engines by Kind.
	engines map[apitypes.DataEngine]data.Engine
	box     *secrets.Box
	roles   data.Roles
	words   Words
	log     *slog.Logger
	// browse throttles data.browse and data.reveal events; deciding keeps
	// two decisions on one changeset from running at once.
	browse   browseLog
	deciding decisions
	// flights shares concurrent identical insights reads.
	flights flights
}

// New returns a Panel. Zero Roles are Dock's (data.DockRoles); a nil
// Logger is slog.Default.
func New[D any](o Options[D]) *Panel[D] {
	if o.Roles == (data.Roles{}) {
		o.Roles = data.DockRoles
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	engines := make(map[apitypes.DataEngine]data.Engine, len(o.Engines))
	for _, e := range o.Engines {
		engines[e.Kind()] = e
	}
	return &Panel[D]{host: o.Host, engines: engines, box: o.Box, roles: o.Roles, words: o.Words, log: o.Logger}
}

// Forget drops the engines' connections and schema caches for a database
// whose connection details or container changed.
func (p *Panel[D]) Forget(id int64) {
	for _, e := range p.engines {
		e.Forget(id)
	}
}

// Close closes every database connection; hosts call it on shutdown.
func (p *Panel[D]) Close() {
	for _, e := range p.engines {
		e.Close()
	}
}

// redactedText stands in for a statement whose engine the panel can't
// redact with (its database is gone, or its engine isn't supported here).
const redactedText = "(values hidden)"

// redact is engine's Redact of text.
func (p *Panel[D]) redact(engine apitypes.DataEngine, text string) string {
	if e, ok := p.engines[engine]; ok {
		return e.Redact(text)
	}
	return redactedText
}

func now() time.Time { return time.Now().UTC() }

// target is a database prepared for engine calls: its setup record and,
// once connected, the engine target and the sensitive columns.
type target[D any] struct {
	db *Database[D]
	// eng is the database's engine.
	eng data.Engine
	// access is the setup record; nil before setup.
	access *Access
	// conn and t are set by connect (useConn).
	conn Conn
	t    data.Target
	// sensitive are the marked columns, sensitiveKeys a key-value
	// database's marked patterns (loadSensitive).
	sensitive     []apitypes.DataColumnRef
	sensitiveKeys []string
}

// load returns db with its engine and setup record.
func (p *Panel[D]) load(ctx context.Context, db *Database[D]) (*target[D], error) {
	eng, ok := p.engines[db.Engine]
	if !ok {
		return nil, invalid("%s is a %s database, which the data panel doesn't support here", db.Name, db.Engine)
	}
	acc, err := p.host.Access(ctx, db.ID)
	if err != nil {
		return nil, err
	}
	return &target[D]{db: db, eng: eng, access: acc}, nil
}

// tables is tg's engine as a tabular one, or a validation error saying
// the database has no tables (doing is what the caller tried).
func tables[D any](tg *target[D], doing string) (data.Tables, error) {
	t, ok := tg.eng.(data.Tables)
	if !ok {
		return nil, invalid("%s is %s: it has no tables, so %s doesn't apply", tg.db.Name, tg.db.Engine, doing)
	}
	return t, nil
}

// qualifyTable names a bare table ("orders") as schema.name from the
// cached schema: Postgres's public first, else the one table of that name,
// as MySQL's schemas are its databases (shop.orders). Qualified names, and
// names it can't settle, pass through for the engine to judge.
func qualifyTable[D any](ctx context.Context, t data.Tables, tg *target[D], call data.Call, name string) (string, error) {
	if strings.Contains(name, ".") {
		return name, nil
	}
	s, err := t.Schema(ctx, tg.t, call, tg.sensitive)
	if err != nil {
		return "", err
	}
	return QualifyTable(s.Tables, name)
}

// QualifyTable names a table schema.name from a bare name among tables:
// public's first, else the one schema that has it (MySQL's schemas are its
// databases). A name in several schemas, or a schema-qualified one, is
// for the caller to name; one in none is returned as given, for the
// engine's own error.
func QualifyTable(tables []apitypes.DataTable, name string) (string, error) {
	if strings.Contains(name, ".") {
		return name, nil
	}
	var found []string
	for _, tb := range tables {
		if tb.Name != name {
			continue
		}
		if tb.Schema == "public" {
			return "public." + name, nil
		}
		found = append(found, tb.Schema+"."+name)
	}
	switch len(found) {
	case 0:
		return name, nil
	case 1:
		return found[0], nil
	}
	return "", Errorf(apitypes.CodeValidation, "table %s is in several schemas (%s); name it schema.table", name, strings.Join(found, ", "))
}

// ready is everything an engine call needs: a level check, a set-up
// check, connect and, when the grants were never synced, a sync.
func (p *Panel[D]) ready(ctx context.Context, db *Database[D], need apitypes.DataLevel, action string) (*target[D], error) {
	if err := RequireLevel(db.Perm, need, action); err != nil {
		return nil, err
	}
	tg, err := p.load(ctx, db)
	if err != nil {
		return nil, err
	}
	if tg.access == nil {
		return nil, p.errNotSetUp(db)
	}
	if err := p.connect(ctx, tg); err != nil {
		return nil, err
	}
	if tg.access.GrantsSyncedHash == "" {
		// Never synced: an upgrade from the predefined-role grants (or a
		// sync that failed). Nothing reads before the new grants are in.
		if err := p.syncGrants(ctx, tg, ""); err != nil {
			return nil, err
		}
	}
	return tg, nil
}

// errNotSetUp says data access isn't set up and who can fix it.
func (p *Panel[D]) errNotSetUp(db *Database[D]) error {
	return conflict("data access is not set up for %s; %s", db.Name, p.words.SetupHint)
}

// connect asks the host how to reach the database, opens the role
// passwords and loads the sensitive columns.
func (p *Panel[D]) connect(ctx context.Context, tg *target[D]) error {
	conn, err := p.host.Connect(ctx, tg.db.Host)
	if errors.Is(err, ErrNotRunning) {
		return conflict("%s is not running; deploy or restart it to use its data", tg.db.Name)
	}
	if err != nil {
		return err
	}
	if err := p.useConn(tg, conn); err != nil {
		return err
	}
	return p.loadSensitive(ctx, tg)
}

// useConn builds the engine target for a connection, with the role
// passwords once set up.
func (p *Panel[D]) useConn(tg *target[D], conn Conn) error {
	tg.conn = conn
	tg.t = data.Target{ServiceID: tg.db.ID, Dial: conn.Dial, TLS: conn.TLS, TLSOptional: conn.TLSOptional, Database: conn.Database}
	if tg.access != nil {
		readerAAD, writerAAD := accessAADs(tg.db.ID)
		reader, err := p.box.Open(tg.access.ReaderSecret, readerAAD)
		if err != nil {
			return fmt.Errorf("open reader password: %w", err)
		}
		writer, err := p.box.Open(tg.access.WriterSecret, writerAAD)
		if err != nil {
			return fmt.Errorf("open writer password: %w", err)
		}
		p.setRoles(tg, string(reader), string(writer))
	}
	return nil
}

// setRoles puts the roles' logins on the target; the masked reader's
// password is derived from the reader's.
func (p *Panel[D]) setRoles(tg *target[D], reader, writer string) {
	tg.t.Reader = data.Credentials{User: string(p.roles.Reader), Password: reader}
	tg.t.Writer = data.Credentials{User: string(p.roles.Writer), Password: writer}
	tg.t.ReaderMasked = data.Credentials{User: string(p.roles.ReaderMasked), Password: p.derivedPassword(reader, p.roles.ReaderMasked)}
	tg.t.Monitor = data.Credentials{User: string(p.roles.Monitor), Password: p.derivedPassword(reader, p.roles.Monitor)}
}

// loadSensitive loads the database's sensitive columns, or for a
// key-value database its sensitive key patterns.
func (p *Panel[D]) loadSensitive(ctx context.Context, tg *target[D]) error {
	if _, ok := tg.eng.(data.Keys); ok {
		keys, err := p.host.SensitiveKeys(ctx, tg.db.ID)
		if err != nil {
			return err
		}
		tg.sensitive, tg.sensitiveKeys = []apitypes.DataColumnRef{}, keys
		return nil
	}
	cols, err := p.host.SensitiveColumns(ctx, tg.db.ID)
	if err != nil {
		return err
	}
	if cols == nil {
		cols = []apitypes.DataColumnRef{}
	}
	tg.sensitive = cols
	return nil
}

// newRolePassword generates a role's password: 32 random bytes as
// URL-safe base64, printable ASCII as the engine requires.
func newRolePassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// derivedPassword derives the masked reader's and the monitor's
// passwords from the reader's, so they need no column of their own and
// rotate with it. HMAC keeps the reader's password from being recovered
// from them.
func (p *Panel[D]) derivedPassword(readerPassword string, role data.Role) string {
	m := hmac.New(sha256.New, []byte(readerPassword))
	m.Write([]byte(role))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// call is the engine call context for the actor. Sensitive columns are
// masked unless the actor may see them (sensitive visible) and reveals
// them: explicitly for pages (DataPageRequest.Reveal, audited as
// data.reveal), implicitly for SQL and changesets, whose every statement
// is audited with its text.
func (tg *target[D]) call(a Actor, reveal bool) data.Call {
	return data.Call{Actor: a.Name, Masked: !reveal || tg.db.Perm.Sensitive != apitypes.SensitiveVisible, Sensitive: tg.sensitive, SensitiveKeys: tg.sensitiveKeys}
}

// dataError maps an engine error to what the caller should do. Errors
// that aren't the engine's pass through.
func (p *Panel[D]) dataError(db *Database[D], err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, data.ErrNotSetUp):
		return conflict("%s's roles in %s are missing or their passwords changed (%v); %s again", p.words.Product, db.Name, err, p.words.SetupHint)
	case errors.Is(err, data.ErrUnsupported):
		return invalid("%v", err)
	case errors.Is(err, data.ErrLoginFailed):
		return invalid("%v. %s", err, p.words.LoginFailedHint)
	case errors.Is(err, data.ErrFull):
		return conflict("%s refused a connection: every connection is taken (%v); restarting it frees them", db.Name, err)
	case errors.Is(err, data.ErrUnreachable):
		return conflict("can't reach %s: %v; check that it is running and healthy", db.Name, err)
	case errors.Is(err, data.ErrNoPrivilege):
		return conflict("%s's roles in %s lack a privilege even after updating their grants (%v); %s again", p.words.Product, db.Name, err, p.words.SetupHint)
	case errors.Is(err, data.ErrNoTable):
		return notFound("%v in %s", err, db.Name)
	case errors.Is(err, data.ErrInvalid):
		return invalid("%v", err)
	}
	return err
}

// syncGrants brings the roles' grants (data.Engine.SyncGrants) up to date
// when the schema or the sensitive columns changed since the last sync
// (current is their GrantsHash, "" to sync regardless), as the owner. A
// failure is an error: masked callers must not run SQL on stale grants.
func (p *Panel[D]) syncGrants(ctx context.Context, tg *target[D], current string) error {
	if current != "" && current == tg.access.GrantsSyncedHash {
		return nil
	}
	hash, err := tg.eng.SyncGrants(ctx, tg.t, tg.conn.Owner, tg.sensitive)
	if err != nil {
		return conflict("couldn't update the grants of %s's roles in %s (%v); %s again", p.words.Product, tg.db.Name, p.dataError(tg.db, err), p.words.SetupHint)
	}
	if err := p.host.SetGrantsHash(ctx, tg.db.ID, hash); err != nil {
		return err
	}
	tg.access.GrantsSyncedHash = hash
	return nil
}

// ensureGrants syncs the grants unless they were synced for the schema the
// engine reports now (cached for data.SchemaTTL, so usually without a
// round trip). Engines without tables have no column grants to sync.
func (p *Panel[D]) ensureGrants(ctx context.Context, tg *target[D], call data.Call) error {
	t, ok := tg.eng.(data.Tables)
	if !ok {
		return nil
	}
	s, err := t.Schema(ctx, tg.t, call, tg.sensitive)
	if err != nil {
		return p.dataError(tg.db, err)
	}
	return p.syncGrants(ctx, tg, data.GrantsHash(s.Tables))
}

// withGrants runs fn and, when it fails for a missing privilege (a
// relation another role created since the last sync), syncs the grants
// and runs it once more.
func (p *Panel[D]) withGrants(ctx context.Context, tg *target[D], fn func() error) error {
	err := fn()
	if !errors.Is(err, data.ErrNoPrivilege) {
		return err
	}
	if err := p.syncGrants(ctx, tg, ""); err != nil {
		return err
	}
	return fn()
}

// rereadOnMiss runs fn and, when it names a table the cached schema
// lacks (data.ErrNoTable), has the engine read the catalog again and runs
// fn once more: the table may be newer than the cache (a migration, a
// restore). Engines without tables, and a cache read less than
// data.SchemaReread ago, leave fn's error as it is.
func rereadOnMiss[D any](tg *target[D], fn func() error) error {
	err := fn()
	if !errors.Is(err, data.ErrNoTable) {
		return err
	}
	if t, ok := tg.eng.(data.Tables); !ok || !t.RereadSchema(tg.t.ServiceID) {
		return err
	}
	return fn()
}

// Info returns the database's panel state and the caller's access; UsedBy
// is empty for the host to fill. The database is only contacted for
// callers with level schema or Manage; others get what the host knows
// without it.
func (p *Panel[D]) Info(ctx context.Context, _ Actor, db *Database[D]) (apitypes.DataInfo, error) {
	tg, err := p.load(ctx, db)
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	return p.info(ctx, tg)
}

// info works out a loaded database's status.
func (p *Panel[D]) info(ctx context.Context, tg *target[D]) (apitypes.DataInfo, error) {
	db := tg.db
	info := apitypes.DataInfo{Engine: db.Engine, DetectedBy: db.DetectedBy, Access: db.Perm, UsedBy: []string{}}
	if tg.access != nil {
		info.SetUpAt, info.SetUpBy, info.ServerVersion = &tg.access.SetUpAt, tg.access.SetUpBy, tg.access.ServerVersion
	}
	conn, err := p.host.Connect(ctx, db.Host)
	switch {
	case errors.Is(err, ErrNotRunning):
		info.Status, info.Reason = apitypes.DataNotRunning, fmt.Sprintf("%s is not running; deploy or restart it", db.Name)
		return info, nil
	case CodeOf(err) == apitypes.CodeValidation:
		info.Status, info.Reason = apitypes.DataUnreachable, err.Error()
		return info, nil
	case err != nil:
		return info, err
	case tg.access == nil:
		info.Status, info.Reason = apitypes.DataSetupNeeded, "Not set up. "+upperFirst(p.words.SetupHint)+"."
		if db.Perm.Manage {
			info.Reason = fmt.Sprintf("Not set up. %s to create %s's roles in the database.", p.words.SetupAction, p.words.Product)
		}
		return info, nil
	case !db.Perm.Level.Includes(apitypes.LevelSchema) && !db.Perm.Manage:
		info.Status = apitypes.DataReady
		return info, nil
	}
	if err := p.useConn(tg, conn); err != nil {
		return info, err
	}
	if tg.access.GrantsSyncedHash == "" {
		// Never synced (an upgrade from the predefined-role grants, or a
		// sync that failed): try now, as the owner. If it fails, Status
		// says why (unreachable, or roles that still have the old grants
		// and need setup again).
		if err := p.loadSensitive(ctx, tg); err != nil {
			return info, err
		}
		if err := p.syncGrants(ctx, tg, ""); err != nil {
			if ctx.Err() != nil {
				return info, ctx.Err()
			}
			p.log.Warn("sync data grants", "database", db.Name, "err", err)
		}
	}
	srv, err := tg.eng.Status(ctx, tg.t)
	if errors.Is(err, data.ErrNoPrivilege) {
		// The panel's roles lost their privileges or vanished (Redis keeps
		// ACL users in memory: a restart drops them). Sync them, as the
		// owner and with the sensitive marks, and look again.
		if err = p.loadSensitive(ctx, tg); err == nil {
			if err = p.syncGrants(ctx, tg, ""); err == nil {
				srv, err = tg.eng.Status(ctx, tg.t)
			}
		}
	}
	switch {
	case err == nil:
		info.Status, info.ServerVersion = apitypes.DataReady, srv.Version
	case errors.Is(err, data.ErrNotSetUp):
		info.Status, info.Reason = apitypes.DataSetupNeeded, p.dataError(db, err).Error()
	case errors.Is(err, data.ErrUnsupported):
		info.Status, info.Reason = apitypes.DataUnsupported, p.dataError(db, err).Error()
	case ctx.Err() != nil:
		return info, ctx.Err()
	default:
		info.Status, info.Reason = apitypes.DataUnreachable, p.dataError(db, err).Error()
	}
	return info, nil
}

// upperFirst capitalizes a hint's first letter to start a sentence.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Setup creates or rotates the panel's roles in the database with the
// owner's credentials ("Set up data access"), stores their passwords
// sealed and syncs the roles' grants. Needs Manage.
func (p *Panel[D]) Setup(ctx context.Context, a Actor, db *Database[D]) (apitypes.DataInfo, error) {
	if !db.Perm.Manage {
		return apitypes.DataInfo{}, forbidden("setting up data access is for admins (%s)", Why(db.Perm))
	}
	tg, err := p.load(ctx, db)
	if err != nil {
		return apitypes.DataInfo{}, err
	}
	if err := p.connect(ctx, tg); err != nil {
		return apitypes.DataInfo{}, err
	}
	p.setRoles(tg, newRolePassword(), newRolePassword())
	srv, err := tg.eng.Setup(ctx, tg.t, tg.conn.Owner, tg.t.Reader, tg.t.Writer)
	if err != nil {
		return apitypes.DataInfo{}, p.dataError(db, err)
	}
	acc := Access{Engine: db.Engine, ServerVersion: srv.Version, SetUpAt: now(), SetUpBy: a.Name}
	readerAAD, writerAAD := accessAADs(db.ID)
	if acc.ReaderSecret, err = p.box.Seal([]byte(tg.t.Reader.Password), readerAAD); err != nil {
		return apitypes.DataInfo{}, err
	}
	if acc.WriterSecret, err = p.box.Seal([]byte(tg.t.Writer.Password), writerAAD); err != nil {
		return apitypes.DataInfo{}, err
	}
	if err := p.host.PutAccess(ctx, db.ID, acc); err != nil {
		return apitypes.DataInfo{}, err
	}
	tg.access = &acc
	p.audit(ctx, a, db, apitypes.AuditDataSetup, fmt.Sprintf("set up data access on %s: created or rotated %s, %s, %s and %s",
		apitypes.ServerLabel(db.Engine, srv.Version), p.roles.Reader, p.roles.Writer, p.roles.ReaderMasked, p.roles.Monitor), nil)
	if err := p.loadSensitive(ctx, tg); err != nil {
		return apitypes.DataInfo{}, err
	}
	if err := p.syncGrants(ctx, tg, ""); err != nil {
		return apitypes.DataInfo{}, err
	}
	return p.info(ctx, tg)
}

// Schema returns the database's structure, sensitive columns marked.
// table, when set, is a table the caller looks for (bare or
// schema.name): a cached schema without it is read again
// (rereadOnMiss). Needs level schema.
func (p *Panel[D]) Schema(ctx context.Context, a Actor, db *Database[D], table string) (apitypes.DataSchema, error) {
	tg, err := p.ready(ctx, db, apitypes.LevelSchema, "reading the schema")
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	if k, ok := tg.eng.(data.Keys); ok {
		var s apitypes.DataSchema
		err := p.withGrants(ctx, tg, func() (err error) {
			s, err = k.Keyspace(ctx, tg.t, tg.call(a, false))
			return err
		})
		s.SensitiveKeys = tg.sensitiveKeys
		return s, p.dataError(db, err)
	}
	t, err := tables(tg, "reading the schema")
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	var s apitypes.DataSchema
	err = rereadOnMiss(tg, func() (err error) {
		if s, err = t.Schema(ctx, tg.t, tg.call(a, false), tg.sensitive); err != nil || table == "" {
			return err
		}
		if !slices.ContainsFunc(s.Tables, func(tb apitypes.DataTable) bool { return tb.Name == table || tb.Schema+"."+tb.Name == table }) {
			return data.ErrNoTable
		}
		return nil
	})
	if errors.Is(err, data.ErrNoTable) {
		err = nil // the caller says it isn't there
	}
	return s, p.dataError(db, err)
}

// Rows returns one keyset page of a table. Needs level read. Its cursors
// are sealed for the caller (sealCursor). Sensitive columns are masked
// unless the request reveals them, which needs sensitive visible and is
// audited as data.reveal before the values go out. The first page per
// caller and table, then at most every 10 minutes, is audited as
// data.browse; reveals are throttled the same way.
func (p *Panel[D]) Rows(ctx context.Context, a Actor, db *Database[D], req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	switch {
	case req.Limit < 0 || req.Limit > apitypes.DataPageMaxLimit:
		return apitypes.DataPage{}, invalid("limit must be between 1 and %d", apitypes.DataPageMaxLimit)
	case req.Limit == 0:
		req.Limit = apitypes.DataPageLimit
	}
	if req.Full && req.Limit != 1 {
		return apitypes.DataPage{}, invalid("full values are only returned one row at a time; set limit to 1")
	}
	tg, err := p.ready(ctx, db, apitypes.LevelRead, "reading rows")
	if err != nil {
		return apitypes.DataPage{}, err
	}
	t, err := tables(tg, "reading rows")
	if err != nil {
		return apitypes.DataPage{}, err
	}
	if req.Reveal && db.Perm.Sensitive != apitypes.SensitiveVisible {
		return apitypes.DataPage{}, forbidden("revealing sensitive columns needs sensitive visible; yours are masked (%s)", Why(db.Perm))
	}
	if req.After, err = p.openCursor(a, db.ID, req.After); err != nil {
		return apitypes.DataPage{}, err
	}
	var page apitypes.DataPage
	name := req.Table
	err = rereadOnMiss(tg, func() error {
		table, err := qualifyTable(ctx, t, tg, tg.call(a, false), name)
		if err != nil {
			return err
		}
		req.Table = table
		return p.withGrants(ctx, tg, func() (err error) {
			page, err = t.Page(ctx, tg.t, tg.call(a, req.Reveal), req)
			return err
		})
	})
	if err != nil {
		return page, p.dataError(db, err)
	}
	if page.Next, err = p.sealCursor(a, db.ID, page.Next); err != nil {
		return apitypes.DataPage{}, err
	}
	p.auditBrowse(ctx, a, db, req)
	if req.Reveal {
		p.auditReveal(ctx, a, tg, req.Table)
	}
	return page, nil
}

// References returns the rows referencing one row. Needs level read.
// Masked callers can't look up a row by a sensitive key, and don't get
// foreign keys over sensitive columns (data.Engine.References).
func (p *Panel[D]) References(ctx context.Context, a Actor, db *Database[D], req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	switch {
	case req.Limit < 0 || req.Limit > apitypes.DataReferencesMaxLimit:
		return nil, invalid("limit must be between 1 and %d", apitypes.DataReferencesMaxLimit)
	case req.Limit == 0:
		req.Limit = apitypes.DataReferencesLimit
	}
	tg, err := p.ready(ctx, db, apitypes.LevelRead, "reading rows")
	if err != nil {
		return nil, err
	}
	t, err := tables(tg, "following references")
	if err != nil {
		return nil, err
	}
	var refs []apitypes.DataReferences
	err = rereadOnMiss(tg, func() error {
		return p.withGrants(ctx, tg, func() (err error) {
			refs, err = t.References(ctx, tg.t, tg.call(a, false), req)
			return err
		})
	})
	if err != nil {
		return nil, p.dataError(db, err)
	}
	for i := range refs {
		if refs[i].Page.Next, err = p.sealCursor(a, db.ID, refs[i].Page.Next); err != nil {
			return nil, err
		}
	}
	p.auditBrowse(ctx, a, db, apitypes.DataPageRequest{Table: req.Table})
	return refs, nil
}

// Query runs one read-only statement, streamed as frames. Needs level
// read. The roles' grants are brought up to date first; masked callers'
// SQL runs as the masked reader. Every statement is audited as data.query
// with its SQL, rows, duration and outcome. The stream calls ready with
// its first frame, so an error before any frame (dial, login, a rejected
// statement) is a plain error.
func (p *Panel[D]) Query(ctx context.Context, a Actor, db *Database[D], req apitypes.DataQueryRequest) (Stream[apitypes.DataQueryFrame], error) {
	switch {
	case strings.TrimSpace(req.SQL) == "":
		return nil, invalid("the SQL is empty")
	case len(req.SQL) > apitypes.DataSQLMaxBytes:
		return nil, Errorf(apitypes.CodeTooLarge, "the SQL is longer than %d bytes", apitypes.DataSQLMaxBytes)
	case req.Limit < 0 || req.Limit > apitypes.DataQueryMaxLimit:
		return nil, invalid("limit must be between 1 and %d", apitypes.DataQueryMaxLimit)
	case req.TimeoutSeconds < 0 || req.TimeoutSeconds > int(apitypes.DataQueryMaxTimeout.Seconds()):
		return nil, invalid("timeout must be between 1 and %d seconds", int(apitypes.DataQueryMaxTimeout.Seconds()))
	}
	tg, err := p.ready(ctx, db, apitypes.LevelRead, "running SQL")
	if err != nil {
		return nil, err
	}
	if len(tg.sensitiveKeys) > 0 && db.Perm.Sensitive != apitypes.SensitiveVisible {
		// Redis ACLs can't deny a key pattern: the console could read
		// what the browser masks.
		return nil, forbidden("the console isn't available while sensitive keys are masked for you (%s)", Why(db.Perm))
	}
	call := tg.call(a, true)
	if err := p.ensureGrants(ctx, tg, call); err != nil {
		return nil, err
	}
	return func(ctx context.Context, ready func(), send func(...apitypes.DataQueryFrame) error) error {
		started := false
		run := func() (data.QueryStats, error) {
			return tg.eng.Query(ctx, tg.t, call, req, func(f apitypes.DataQueryFrame) error {
				if !started {
					ready()
					started = true
				}
				return send(f)
			})
		}
		stats, err := run()
		if errors.Is(err, data.ErrNoPrivilege) && !started {
			// A login refused before anything was sent (Redis' users went
			// with a restart): sync the roles and run once more.
			if serr := p.syncGrants(ctx, tg, ""); serr == nil {
				stats, err = run()
			}
		}
		p.auditQuery(ctx, a, db, req.SQL, stats, err)
		return p.dataError(db, err)
	}, nil
}

// History returns the caller's own queries on the database, newest first,
// before the given audit ID (0: from the newest), at most limit (1-200,
// else 50). Needs level read.
func (p *Panel[D]) History(ctx context.Context, a Actor, db *Database[D], before int64, limit int) ([]apitypes.DataHistoryEntry, error) {
	if err := RequireLevel(db.Perm, apitypes.LevelRead, "reading query history"); err != nil {
		return nil, err
	}
	if before < 0 {
		before = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return p.host.QueryHistory(ctx, a, db.Host, before, limit)
}

// savedQueryOwner is the user whose saved queries the caller uses. Needs
// level read.
func (p *Panel[D]) savedQueryOwner(ctx context.Context, a Actor, db *Database[D]) (int64, error) {
	user, err := p.host.SavedQueryOwner(ctx, a)
	if err != nil {
		return 0, err
	}
	if user == 0 {
		return 0, invalid("saved queries belong to users; sign in as a user to use them")
	}
	return user, RequireLevel(db.Perm, apitypes.LevelRead, "using saved queries")
}

// SavedQueries returns the caller's saved queries for the database, by
// name.
func (p *Panel[D]) SavedQueries(ctx context.Context, a Actor, db *Database[D]) ([]apitypes.DataSavedQuery, error) {
	user, err := p.savedQueryOwner(ctx, a, db)
	if err != nil {
		return nil, err
	}
	return p.host.SavedQueries(ctx, db.ID, user)
}

// SaveQuery creates (id 0) or replaces one of the caller's saved queries.
func (p *Panel[D]) SaveQuery(ctx context.Context, a Actor, db *Database[D], id int64, req apitypes.SaveDataQueryRequest) (apitypes.DataSavedQuery, error) {
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case req.Name == "" || len(req.Name) > 100:
		return apitypes.DataSavedQuery{}, invalid("a saved query needs a name of 1-100 characters")
	case strings.TrimSpace(req.SQL) == "":
		return apitypes.DataSavedQuery{}, invalid("the SQL is empty")
	case len(req.SQL) > apitypes.DataSQLMaxBytes:
		return apitypes.DataSavedQuery{}, Errorf(apitypes.CodeTooLarge, "the SQL is longer than %d bytes", apitypes.DataSQLMaxBytes)
	}
	user, err := p.savedQueryOwner(ctx, a, db)
	if err != nil {
		return apitypes.DataSavedQuery{}, err
	}
	q, err := p.host.SaveQuery(ctx, db.ID, user, id, req.Name, req.SQL, now())
	switch {
	case errors.Is(err, ErrNotFound):
		return apitypes.DataSavedQuery{}, notFound("saved query %d not found", id)
	case errors.Is(err, ErrDuplicate):
		return apitypes.DataSavedQuery{}, conflict("you already have a saved query named %q", req.Name)
	}
	return q, err
}

// DeleteSavedQuery deletes one of the caller's saved queries.
func (p *Panel[D]) DeleteSavedQuery(ctx context.Context, a Actor, db *Database[D], id int64) error {
	user, err := p.savedQueryOwner(ctx, a, db)
	if err != nil {
		return err
	}
	ok, err := p.host.DeleteSavedQuery(ctx, db.ID, user, id)
	if err != nil {
		return err
	}
	if !ok {
		return notFound("saved query %d not found", id)
	}
	return nil
}

// columnsExist refuses a column ref the schema doesn't have.
func columnsExist(s apitypes.DataSchema, refs []apitypes.DataColumnRef) error {
	for _, ref := range refs {
		i := slices.IndexFunc(s.Tables, func(t apitypes.DataTable) bool { return t.Schema == ref.Schema && t.Name == ref.Table })
		if i < 0 {
			return invalid("the database has no table %s.%s; name a table as the schema shows it (a new one shows within a minute)", ref.Schema, ref.Table)
		}
		if !slices.ContainsFunc(s.Tables[i].Columns, func(c apitypes.DataColumn) bool { return c.Name == ref.Column }) {
			return invalid("table %s.%s has no column %s", ref.Schema, ref.Table, ref.Column)
		}
	}
	return nil
}

// SetSensitiveColumns replaces the database's sensitive columns and, when
// the database is set up and reachable, checks they exist and syncs the
// roles' grants (otherwise the next masked query does, or refuses). Needs
// Manage.
func (p *Panel[D]) SetSensitiveColumns(ctx context.Context, a Actor, db *Database[D], req apitypes.SetSensitiveColumnsRequest) ([]apitypes.DataColumnRef, error) {
	if !db.Perm.Manage {
		return nil, forbidden("marking sensitive columns is for admins (%s)", Why(db.Perm))
	}
	for _, col := range req.Columns {
		if col.Schema == "" || col.Table == "" || col.Column == "" {
			return nil, invalid("a sensitive column needs its schema, table and column")
		}
	}
	tg, err := p.load(ctx, db)
	if err != nil {
		return nil, err
	}
	// Reachable: every mark names a column the database has. A mark that
	// matches nothing masks nothing, and says so to no one (a MySQL column
	// marked under Postgres's public schema).
	connErr := ErrNotRunning
	if tg.access != nil {
		var conn Conn
		if conn, connErr = p.host.Connect(ctx, db.Host); connErr == nil {
			connErr = p.useConn(tg, conn)
		}
	}
	if t, ok := tg.eng.(data.Tables); ok && connErr == nil {
		if s, err := t.Schema(ctx, tg.t, tg.call(a, false), nil); err == nil {
			if err := columnsExist(s, req.Columns); err != nil {
				return nil, err
			}
		}
	}
	if err := p.host.ReplaceSensitiveColumns(ctx, db.ID, req.Columns); err != nil {
		return nil, err
	}
	if err := p.loadSensitive(ctx, tg); err != nil {
		return nil, err
	}
	if tg.access != nil {
		// Best effort: if it fails, the next masked query syncs or refuses.
		err := connErr
		if err == nil {
			err = p.syncGrants(ctx, tg, "")
		}
		if err != nil && !errors.Is(err, ErrNotRunning) {
			p.log.Warn("sync masked reader after marking sensitive columns", "database", db.Name, "err", err)
		}
	}
	names := make([]string, len(tg.sensitive))
	for i, col := range tg.sensitive {
		names[i] = col.Schema + "." + col.Table + "." + col.Column
	}
	summary := "cleared the sensitive columns"
	if len(names) > 0 {
		summary = "marked sensitive: " + strings.Join(names, ", ")
	}
	p.audit(ctx, a, db, apitypes.AuditDataPolicy, summary, nil)
	return tg.sensitive, nil
}
