package core

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Unauthenticated sign-ins are held per client (an IPv6 /64 counts as
// one), and a store full of them makes room by dropping the oldest, so a
// few addresses can't lock everyone out of passkeys and single sign-on.
func TestCeremoniesPerClient(t *testing.T) {
	cs := newCeremonies()
	put := func(cer *ceremony) string {
		t.Helper()
		id, err := cs.put(cer)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := put(&ceremony{purpose: ceremonySSO, client: "2001:db8:1:2::1"})
	for i := range maxClientCeremonies {
		put(&ceremony{purpose: ceremonySSO, client: fmt.Sprintf("2001:db8:1:2::%x", i+2)})
	}
	if cs.m[first] != nil || len(cs.m) != maxClientCeremonies {
		t.Fatalf("one /64 holds %d, its first still there: %v", len(cs.m), cs.m[first] != nil)
	}

	// Many clients fill the store: the next drops the oldest, and a
	// signed-in person's step does too.
	for i := range maxCeremonies {
		put(&ceremony{purpose: ceremonySSO, client: fmt.Sprintf("198.51.%d.%d", i/200, i%200)})
	}
	mine := put(&ceremony{purpose: ceremonySSO, client: "192.0.2.7"})
	put(&ceremony{purpose: ceremonyTOTP, userID: 7, sessionID: 1})
	if cs.m[mine] == nil || len(cs.m) != maxCeremonies {
		t.Fatalf("full store: %d held, the newest kept: %v", len(cs.m), cs.m[mine] != nil)
	}

	// Only signed-in people's steps filling it refuse a new one.
	full := newCeremonies()
	for i := range maxCeremonies {
		if _, err := full.put(&ceremony{purpose: ceremonyTOTP, userID: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := full.put(&ceremony{purpose: ceremonyLogin, client: "192.0.2.7"}); CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("store full of people mid-step: %v", err)
	}
}
