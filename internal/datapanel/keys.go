package datapanel

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// Key-value databases (Redis, Valkey; ADR 0072): the keyspace, keys and
// one key's value. Key names are data (they embed IDs and emails), so
// both need level read, like rows.

// keys is tg's engine as a key-value one, or a validation error saying
// the database has no keys (doing is what the caller tried).
func keys[D any](tg *target[D], doing string) (data.Keys, error) {
	k, ok := tg.eng.(data.Keys)
	if !ok {
		return nil, invalid("%s is %s: it has tables, not keys, so %s doesn't apply", tg.db.Name, tg.db.Engine, doing)
	}
	return k, nil
}

// Keys reads one page of keys. Needs level read. Its cursor is sealed
// for the caller. The first page per caller and logical database, then at
// most every 10 minutes, is audited as data.browse with the pattern and
// type, never key names.
func (p *Panel[D]) Keys(ctx context.Context, a Actor, db *Database[D], req apitypes.DataKeysRequest) (apitypes.DataKeyPage, error) {
	if req.Limit < 0 || req.Limit > apitypes.DataKeysMaxLimit {
		return apitypes.DataKeyPage{}, invalid("limit must be between 1 and %d", apitypes.DataKeysMaxLimit)
	}
	tg, err := p.ready(ctx, db, apitypes.LevelRead, "reading keys")
	if err != nil {
		return apitypes.DataKeyPage{}, err
	}
	k, err := keys(tg, "reading keys")
	if err != nil {
		return apitypes.DataKeyPage{}, err
	}
	if req.After, err = p.openCursor(a, db.ID, req.After); err != nil {
		return apitypes.DataKeyPage{}, err
	}
	var page apitypes.DataKeyPage
	err = p.withGrants(ctx, tg, func() (err error) {
		page, err = k.ScanKeys(ctx, tg.t, tg.call(a, false), req)
		return err
	})
	if err != nil {
		return page, p.dataError(db, err)
	}
	if page.Next, err = p.sealCursor(a, db.ID, page.Next); err != nil {
		return apitypes.DataKeyPage{}, err
	}
	if p.browse.due(browseKey{apitypes.AuditDataBrowse, ActorKey(a), db.ID, "keys:" + strconv.Itoa(req.DB)}, time.Now()) {
		summary := fmt.Sprintf("browsed keys in db %d", req.DB)
		if req.Match != "" {
			summary += " matching " + req.Match
		}
		if req.Type != "" {
			summary += " of type " + req.Type
		}
		p.audit(context.WithoutCancel(ctx), a, db, apitypes.AuditDataBrowse, summary, nil)
	}
	return page, nil
}

// Key reads one key and a page of its value. Needs level read; audited
// with the keys of its database (Keys).
func (p *Panel[D]) Key(ctx context.Context, a Actor, db *Database[D], req apitypes.DataKeyRequest) (apitypes.DataKeyValue, error) {
	if req.Limit < 0 || req.Limit > apitypes.DataKeyValueMaxLimit {
		return apitypes.DataKeyValue{}, invalid("limit must be between 1 and %d", apitypes.DataKeyValueMaxLimit)
	}
	if req.Key == "" {
		return apitypes.DataKeyValue{}, invalid("name the key")
	}
	tg, err := p.ready(ctx, db, apitypes.LevelRead, "reading keys")
	if err != nil {
		return apitypes.DataKeyValue{}, err
	}
	k, err := keys(tg, "reading a key")
	if err != nil {
		return apitypes.DataKeyValue{}, err
	}
	if req.After, err = p.openCursor(a, db.ID, req.After); err != nil {
		return apitypes.DataKeyValue{}, err
	}
	var out apitypes.DataKeyValue
	err = p.withGrants(ctx, tg, func() (err error) {
		out, err = k.Key(ctx, tg.t, tg.call(a, false), req)
		return err
	})
	if err != nil {
		return out, p.dataError(db, err)
	}
	if out.Next, err = p.sealCursor(a, db.ID, out.Next); err != nil {
		return apitypes.DataKeyValue{}, err
	}
	if p.browse.due(browseKey{apitypes.AuditDataBrowse, ActorKey(a), db.ID, "keys:" + strconv.Itoa(req.DB)}, time.Now()) {
		p.audit(context.WithoutCancel(ctx), a, db, apitypes.AuditDataBrowse, fmt.Sprintf("browsed keys in db %d", req.DB), nil)
	}
	return out, nil
}
