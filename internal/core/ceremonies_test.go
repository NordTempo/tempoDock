package core

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Unauthenticated sign-ins are held per client (an IPv6 /48 counts as
// one), and a store full of them makes room by dropping one, so a few
// addresses can't lock everyone out of passkeys and single sign-on.
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
		put(&ceremony{purpose: ceremonySSO, client: fmt.Sprintf("2001:db8:1:%x::2", i+2)})
	}
	if cs.m[first] != nil || len(cs.m) != maxClientCeremonies {
		t.Fatalf("one /48 holds %d, its first still there: %v", len(cs.m), cs.m[first] != nil)
	}

	// Many clients fill the store: the next drops one, and a signed-in
	// person's step does too.
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

// A full store makes room from the caller's own ceremonies first, and
// for a caller with none from the client holding the most: clients that
// start many replace their own, and a person with one sign-in in flight
// (here at their identity provider) keeps it.
func TestCeremoniesFullStoreKeepsOthersSignIns(t *testing.T) {
	cs := newCeremonies()
	mine, err := cs.put(&ceremony{purpose: ceremonySSO, client: "198.51.100.7", sso: &ssoFlow{}})
	if err != nil {
		t.Fatal(err)
	}
	// 60 clients, 20 each: the store fills after 50 of them, and the
	// last 10 hold none of their own when they start.
	for client := range 60 {
		for range maxClientCeremonies {
			if _, err := cs.put(&ceremony{purpose: ceremonyLogin, client: fmt.Sprintf("2001:db8:%x::1", client)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// And a signed-in person's step, which has no client.
	if _, err := cs.put(&ceremony{purpose: ceremonyTOTP, userID: 7, sessionID: 1}); err != nil {
		t.Fatal(err)
	}
	if len(cs.m) != maxCeremonies {
		t.Fatalf("%d held, want %d", len(cs.m), maxCeremonies)
	}
	if _, err := cs.take(mine, ceremonySSO, 0, 0); err != nil {
		t.Fatalf("a sign-in in flight was dropped for other clients' starts: %v", err)
	}
}
