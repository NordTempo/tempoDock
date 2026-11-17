package core

import (
	"bytes"
	"regexp"
	"testing"

	"github.com/NordTempo/tempoDock/internal/secrets"
)

// An environment's build cache scope is the same for each of its builds,
// its own, in the form the builder accepts, and not something its IDs
// give away: another control plane's key makes another scope.
func TestCacheScope(t *testing.T) {
	h := newHarness(t)
	scope := h.c.cacheScope(1, 2)
	if !regexp.MustCompile(`^p1_e2_[0-9a-f]{16}$`).MatchString(scope) {
		t.Fatalf("scope %q", scope)
	}
	if again := h.c.cacheScope(1, 2); again != scope {
		t.Fatalf("the scope changed between builds: %q, %q", scope, again)
	}
	if other := h.c.cacheScope(1, 3); other == scope || other[len("p1_e3"):] == scope[len("p1_e2"):] {
		t.Fatalf("another environment's scope: %q next to %q", other, scope)
	}
	box, err := secrets.New(bytes.Repeat([]byte{9}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := &Core{box: box}
	if got := elsewhere.cacheScope(1, 2); got == scope {
		t.Fatalf("another key gave the same scope %q", got)
	}
}
