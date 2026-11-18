package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A database nobody has an account in lets groups and grants decide from
// its first start, or from its bootstrap when that comes first. One that
// has people and never set the mode came from before the model and keeps
// shadow, and a mode an operator chose is never written over.
func TestNewInstallEnforces(t *testing.T) {
	h := newHarness(t)
	// The test database starts as an older install's (storetest.Open);
	// without its settings row it is a new one.
	forget := func() {
		t.Helper()
		if _, err := h.store.DB.ExecContext(h.ctx, "DELETE FROM platform_settings"); err != nil {
			t.Fatal(err)
		}
	}
	forget()
	h.restart()
	if h.c.accessMode() != apitypes.AccessModelEnforce {
		t.Fatalf("a new install started in %s", h.c.accessMode())
	}

	// Bootstrapped before the daemon ever started.
	forget()
	if _, ok, err := h.c.Bootstrap(h.ctx, "admin@localhost"); err != nil || !ok {
		t.Fatalf("bootstrap: %t, %v", ok, err)
	}
	h.restart()
	if h.c.accessMode() != apitypes.AccessModelEnforce {
		t.Fatalf("bootstrapped first: %s", h.c.accessMode())
	}

	// An install with people that never set the mode.
	forget()
	h.restart()
	if h.c.accessMode() != apitypes.AccessModelShadow {
		t.Fatalf("an install from before the model started in %s", h.c.accessMode())
	}
	if _, err := h.c.SetAccessModel(h.ctx, System, apitypes.AccessModel{Mode: apitypes.AccessModelShadow}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	if h.c.accessMode() != apitypes.AccessModelShadow {
		t.Fatalf("the operator's shadow became %s", h.c.accessMode())
	}
}
