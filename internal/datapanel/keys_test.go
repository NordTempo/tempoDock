package datapanel

import (
	"context"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// keysEngine is a key-value engine over fakeEngine.
type keysEngine struct{ *fakeEngine }

func (keysEngine) Kind() apitypes.DataEngine { return apitypes.EngineRedis }

func (keysEngine) Keyspace(context.Context, data.Target, data.Call) (apitypes.DataSchema, error) {
	return apitypes.DataSchema{Keyspace: []apitypes.DataKeyspaceDB{{DB: 0, Keys: 2}}}, nil
}

func (keysEngine) ScanKeys(context.Context, data.Target, data.Call, apitypes.DataKeysRequest) (apitypes.DataKeyPage, error) {
	return apitypes.DataKeyPage{}, nil
}

func (keysEngine) Key(_ context.Context, _ data.Target, _ data.Call, req apitypes.DataKeyRequest) (apitypes.DataKeyValue, error) {
	return apitypes.DataKeyValue{DataKeyInfo: apitypes.DataKeyInfo{Key: req.Key, Type: "string"}}, nil
}

// TestSensitiveKeys: admins mark key patterns (never *); a matching key
// reads only with a reveal by a caller who sees sensitive values, which
// is audited; masked callers lose the console while patterns exist.
func TestSensitiveKeys(t *testing.T) {
	p, h, base := newTestPanel(t)
	p.engines = map[apitypes.DataEngine]data.Engine{apitypes.EngineRedis: keysEngine{base}}
	ctx := context.Background()
	redisDB := func(perm apitypes.DataPermission) *Database[string] {
		db := testDB(perm)
		db.Engine = apitypes.EngineRedis
		return db
	}
	if _, err := p.Setup(ctx, admin, redisDB(adminPerm)); err != nil {
		t.Fatal(err)
	}
	reader := apitypes.DataPermission{Level: apitypes.LevelRead, Sensitive: masked}
	if _, err := p.SetSensitiveKeys(ctx, agent, redisDB(reader), apitypes.SetSensitiveKeysRequest{Patterns: []string{"session:*"}}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a reader marked keys: %v", err)
	}
	if _, err := p.SetSensitiveKeys(ctx, admin, redisDB(adminPerm), apitypes.SetSensitiveKeysRequest{Patterns: []string{"*"}}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("* accepted: %v", err)
	}
	got, err := p.SetSensitiveKeys(ctx, admin, redisDB(adminPerm), apitypes.SetSensitiveKeysRequest{Patterns: []string{" session:* ", "session:*", ""}})
	if err != nil || len(got) != 1 || got[0] != "session:*" {
		t.Fatalf("patterns: %v %v", got, err)
	}

	if _, err := p.Key(ctx, agent, redisDB(reader), apitypes.DataKeyRequest{Key: "user:1"}); err != nil {
		t.Fatalf("an unmarked key: %v", err)
	}
	if _, err := p.Key(ctx, agent, redisDB(reader), apitypes.DataKeyRequest{Key: "session:1", Reveal: true}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a masked caller revealed: %v", err)
	}
	if _, err := p.Key(ctx, admin, redisDB(adminPerm), apitypes.DataKeyRequest{Key: "session:1"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a sensitive key without reveal: %v", err)
	}
	h.audits = nil
	if _, err := p.Key(ctx, admin, redisDB(adminPerm), apitypes.DataKeyRequest{Key: "session:1", Reveal: true}); err != nil {
		t.Fatalf("reveal: %v", err)
	}
	revealed := false
	for _, a := range h.audits {
		revealed = revealed || a.Action == apitypes.AuditDataReveal
	}
	if !revealed {
		t.Errorf("reveal not audited: %+v", h.audits)
	}
	if _, err := p.Query(ctx, agent, redisDB(reader), apitypes.DataQueryRequest{SQL: "GET session:1"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("console for a masked caller: %v", err)
	}
	if s, err := p.Schema(ctx, agent, redisDB(reader)); err != nil || len(s.SensitiveKeys) != 1 {
		t.Fatalf("schema: %+v %v", s, err)
	}
}
