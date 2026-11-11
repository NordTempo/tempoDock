package datapanel

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
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
		// Names of sensitive keys are masked only for callers who see
		// sensitive values masked: a name is how a key is opened (and
		// revealed).
		page, err = k.ScanKeys(ctx, tg.t, tg.call(a, true), req)
		return err
	})
	if err != nil {
		return page, p.dataError(db, err)
	}
	// A masked key says only that a key matching its pattern exists: not
	// its size, and nothing under a match, whose answers would spell its
	// name out a character at a time (session:a*, session:ab*, ...).
	page.Keys = slices.DeleteFunc(page.Keys, func(k apitypes.DataKeyInfo) bool { return k.Masked && req.Match != "" })
	for i := range page.Keys {
		if page.Keys[i].Masked {
			page.Keys[i].Length, page.Keys[i].Bytes = 0, 0
		}
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
// with the keys of its database (Keys). A key matching a sensitive
// pattern needs req.Reveal, which needs sensitive visible and is audited
// as data.reveal (at most every 10 minutes per caller and pattern).
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
	name := req.Key
	if req.Hex {
		if b, err := hex.DecodeString(req.Key); err == nil {
			name = string(b)
		}
	}
	pattern, sensitive := data.KeySensitive(tg.sensitiveKeys, name)
	if sensitive {
		switch {
		case db.Perm.Sensitive != apitypes.SensitiveVisible:
			return apitypes.DataKeyValue{}, forbidden("the key matches the sensitive pattern %s, which is masked for you (%s)", pattern, Why(db.Perm))
		case !req.Reveal:
			return apitypes.DataKeyValue{}, forbidden("the key matches the sensitive pattern %s; reveal it to read it (audited)", pattern)
		}
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
	if sensitive && p.browse.due(browseKey{apitypes.AuditDataReveal, ActorKey(a), db.ID, "keys:" + pattern}, time.Now()) {
		p.audit(context.WithoutCancel(ctx), a, db, apitypes.AuditDataReveal, fmt.Sprintf("revealed keys matching %s in db %d", pattern, req.DB), nil)
	}
	return out, nil
}

// SetSensitiveKeys replaces a key-value database's sensitive key
// patterns. Admins (Manage); audited as data.policy.
func (p *Panel[D]) SetSensitiveKeys(ctx context.Context, a Actor, db *Database[D], req apitypes.SetSensitiveKeysRequest) ([]string, error) {
	if !db.Perm.Manage {
		return nil, forbidden("marking sensitive keys is for admins (%s)", Why(db.Perm))
	}
	tg, err := p.load(ctx, db)
	if err != nil {
		return nil, err
	}
	if _, err := keys(tg, "marking sensitive keys"); err != nil {
		return nil, err
	}
	patterns := make([]string, 0, len(req.Patterns))
	for _, pat := range req.Patterns {
		pat = strings.TrimSpace(pat)
		switch {
		case pat == "":
			continue
		case pat == "*":
			return nil, invalid("* would mask every key; mark the patterns of the sensitive ones")
		case len(pat) > 200:
			return nil, invalid("a pattern is at most 200 characters")
		}
		if !slices.Contains(patterns, pat) {
			patterns = append(patterns, pat)
		}
	}
	if err := p.host.ReplaceSensitiveKeys(ctx, db.ID, patterns); err != nil {
		return nil, err
	}
	summary := "cleared the sensitive keys"
	if len(patterns) > 0 {
		summary = "marked sensitive keys: " + strings.Join(patterns, ", ")
	}
	p.audit(ctx, a, db, apitypes.AuditDataPolicy, summary, nil)
	return patterns, nil
}
