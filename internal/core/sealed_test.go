package core

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/secrets"
)

// A sealed value pasted into another row doesn't open there: someone who
// can write the database can't move a secret to where it is shown.
func TestSealedValueStaysInItsRow(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.set(web.Env(), "web", map[string]string{"SECRET": "hunter2", "SHOWN": "hello"}, false)
	h.exec(`UPDATE variables SET value = pending_value, pending = '', pending_value = NULL`)
	h.exec(`UPDATE variables SET value = (SELECT value FROM variables WHERE key = 'SECRET') WHERE key = 'SHOWN'`)

	vars, err := h.c.ListVariables(h.ctx, testSystem, web.Env(), "web", true)
	if CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "SHOWN") {
		t.Fatalf("revealed a moved value, or didn't say which: %+v, %v", vars, err)
	}
}

// Values sealed before ADR 0041 are bound on start, once, and the same
// way every time; from then on an unbound value doesn't open.
func TestResealSecrets(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.SetOrgVariable(h.ctx, testSystem, "SENTRY_DSN", "https://sentry"); err != nil {
		t.Fatal(err)
	}
	h.set(web.Env(), "web", map[string]string{"TOKEN": "t1", "DSN": "${{org.SENTRY_DSN}}"}, false)
	h.deploy(web, "app:1")                                           // carries TOKEN and DSN
	h.set(web.Env(), "web", map[string]string{"TOKEN": "t2"}, false) // staged
	h.channel(apitypes.CreateAlertChannelRequest{Name: "ops", Kind: apitypes.ChannelSlack, URL: "https://hooks.example/x"})

	// Back to how an install before ADR 0041 stored them. Opening each
	// value with its column's AAD checks sealedColumns against the AADs
	// the code seals with.
	for _, col := range sealedColumns {
		h.unbind(col)
	}
	h.unbindCarried()
	h.exec(`UPDATE install SET secrets_format = 1`)
	for _, col := range []string{"variables.value", "variables.pending_value", "org_variables.value", "releases.config", "alert_channels.config"} {
		if h.unbound(col) == 0 {
			t.Fatalf("no unbound values in %s to reseal", col)
		}
	}

	h.restart()
	for _, col := range sealedColumns {
		if n := h.unbound(col.Table + "." + col.Name); n != 0 {
			t.Errorf("%s.%s: %d values still unbound", col.Table, col.Name, n)
		}
	}
	vars, err := h.c.ListVariables(h.ctx, testSystem, web.Env(), "web", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if want := map[string]string{"TOKEN": "t1", "DSN": "${{org.SENTRY_DSN}}"}[v.Key]; *v.Value != want {
			t.Errorf("%s = %q, want %q", v.Key, *v.Value, want)
		}
	}
	changes, err := h.c.GetChanges(h.ctx, testSystem, web.Env(), true)
	if err != nil || len(changes.Variables) != 1 || *changes.Variables[0].New != "t2" {
		t.Fatalf("staged = %+v, %v", changes, err)
	}
	if org, err := h.c.ListOrgVariables(h.ctx, testSystem, true); err != nil || *org[0].Value != "https://sentry" {
		t.Fatalf("org variables = %+v, %v", org, err)
	}
	if _, err := h.c.GetRelease(h.ctx, testSystem, web, 1); err != nil {
		t.Fatal(err)
	}
	// A release's record of the change it applied is still the live
	// value's bytes, so restaging finds it.
	live := h.blob(`SELECT value FROM variables WHERE key = 'TOKEN'`)
	ch, _ := decodeCarried(string(h.blob(`SELECT carried FROM releases WHERE number = 1`)))
	for _, v := range ch.Variables {
		if v.Key == "TOKEN" && !bytes.Equal(v.New, live) {
			t.Errorf("carried TOKEN %x, live %x", v.New, live)
		}
	}

	// Again (an interrupted run, or one in another process): the same bytes.
	const all = `SELECT group_concat(hex(value) || hex(coalesce(pending_value, ''))) FROM variables`
	before := h.blob(all)
	h.exec(`UPDATE install SET secrets_format = 1`)
	h.restart()
	if after := h.blob(all); !bytes.Equal(before, after) {
		t.Error("resealing again changed the values")
	}

	// An old value pasted in now doesn't open anywhere.
	h.exec(`UPDATE variables SET value = ? WHERE key = 'TOKEN'`, sealUnbound(t, []byte("t1")))
	if _, err := h.c.ListVariables(h.ctx, testSystem, web.Env(), "web", true); err == nil {
		t.Fatal("an unbound value opened after resealing")
	}
}

// sealUnbound seals as Dock did before ADR 0041, with the harness's key.
func sealUnbound(t *testing.T, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	gcm, _ := cipher.NewGCM(block)
	out := make([]byte, 1+gcm.NonceSize())
	out[0] = 1
	_, _ = rand.Read(out[1:])
	return gcm.Seal(out, out[1:], plaintext, nil)
}

// unbind rewrites col's values as unbound ones, opening each with the AAD
// col makes for its row.
func (h *harness) unbind(col secrets.Column) {
	h.t.Helper()
	sel := append(append([]string{}, col.Key...), col.Name)
	rows, err := h.store.DB.QueryContext(h.ctx, "SELECT "+strings.Join(sel, ", ")+" FROM "+col.Table+" WHERE "+col.Name+" IS NOT NULL")
	if err != nil {
		h.t.Fatal(err)
	}
	var updates [][]any // the unbound value, the old one
	for rows.Next() {
		dest := make([]any, len(sel))
		ptrs := make([]any, len(sel))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			h.t.Fatal(err)
		}
		parts := make([]string, len(col.Key))
		for i := range parts {
			parts[i] = fmt.Sprint(dest[i])
		}
		sealed, _ := dest[len(col.Key)].([]byte)
		plain, err := h.c.box.Open(sealed, col.AAD(parts...))
		if err != nil {
			h.t.Fatalf("%s.%s at %v: %v", col.Table, col.Name, parts, err)
		}
		updates = append(updates, []any{sealUnbound(h.t, plain), sealed})
	}
	if err := rows.Err(); err != nil {
		h.t.Fatal(err)
	}
	_ = rows.Close()
	for _, u := range updates {
		h.exec("UPDATE "+col.Table+" SET "+col.Name+" = ? WHERE "+col.Name+" = ?", u...)
	}
}

// unbindCarried makes the values releases carried the unbound bytes of
// their variables, as they were: a carried value is a copy of the
// variable's.
func (h *harness) unbindCarried() {
	h.t.Helper()
	rows, err := h.store.ListCarriedVariables(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, r := range rows {
		ch, _ := decodeCarried(r.Carried)
		for i, v := range ch.Variables {
			ch.Variables[i].New = h.blob(`SELECT value FROM variables WHERE environment_id = ? AND coalesce(service_id, 0) = coalesce(?, 0) AND key = ?`,
				r.EnvironmentID, v.ServiceID, v.Key)
		}
		enc, _ := ch.encode()
		h.exec(`UPDATE releases SET carried = ? WHERE id = ?`, enc, r.ID)
	}
}

// unbound counts a column's unbound values ("table.column").
func (h *harness) unbound(col string) int {
	h.t.Helper()
	table, name, _ := strings.Cut(col, ".")
	var n int
	if err := h.store.DB.QueryRowContext(h.ctx, "SELECT count(*) FROM "+table+" WHERE substr("+name+", 1, 1) = x'01'").Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// exec runs a statement on the store directly, as someone with write
// access to the database file could.
func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	if _, err := h.store.DB.ExecContext(h.ctx, query, args...); err != nil {
		h.t.Fatal(err)
	}
}

// blob reads one value from the store directly.
func (h *harness) blob(query string, args ...any) []byte {
	h.t.Helper()
	var b []byte
	if err := h.store.DB.QueryRowContext(h.ctx, query, args...).Scan(&b); err != nil {
		h.t.Fatal(err)
	}
	return b
}
