package core

import (
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/httpkit"
)

// Ceremonies are sign-in steps in flight that hold a secret until the
// person answers (ADR 0057): a passkey challenge, an authenticator app's
// new key waiting for its first code, or a single sign-on waiting for the
// identity provider to send the browser back (ADR 0058). They live in
// memory for ceremonyTTL (SSOTTL), bound to the user and session that
// started them (a single sign-on to its browser instead), and are taken
// out when answered, so an answer works once. A restart forgets them; the
// person starts again.
//
// Anyone may start a passkey sign-in or a single sign-on, so those
// (userID 0) are held at most maxClientCeremonies per client
// (httpkit.ClientKey: an IPv4 address or an IPv6 /64), and a full store
// makes room by dropping the oldest of them: a flood from a few
// addresses replaces its own, never locks everyone out.

const (
	ceremonyTTL = 5 * time.Minute
	// SSOTTL is how long a person has at their identity provider; the
	// API's browser binding cookie lasts as long.
	SSOTTL = 10 * time.Minute
	// maxCeremonies bounds how many can be held in memory.
	maxCeremonies = 1000
	// maxClientCeremonies bounds the unauthenticated ones one client
	// holds: a busy office's sign-ins at once, behind one address.
	maxClientCeremonies = 20
)

// Ceremony purposes: what a ceremony may be answered for.
const (
	ceremonyRegister = "register" // adding a passkey
	ceremonyLogin    = "login"    // signing in with a passkey alone
	ceremonySecond   = "second"   // a password sign-in's second factor
	ceremonyReauth   = "reauth"   // a step-up
	ceremonyTOTP     = "totp"     // an authenticator app awaiting its first code
	ceremonySSO      = "sso"      // a single sign-on awaiting the provider's answer
)

// ceremony is a step in flight, bound to the user and session that
// started it (userID 0 for a passkey sign-in, which learns the user from
// the answer; none for a single sign-on, whose sso binds it) and, for
// passkeys, the origin it was asked from. client is the IP that started
// an unauthenticated one.
type ceremony struct {
	purpose   string
	userID    int64
	sessionID int64
	origin    string
	client    string
	webauthn  webauthn.SessionData
	totpKey   []byte
	sso       *ssoFlow
	attempts  int
	started   time.Time
	expires   time.Time
}

// ceremonies holds the ceremonies in flight. No timer: expired ones go
// when the map is full and another is added.
type ceremonies struct {
	mu sync.Mutex
	m  map[string]*ceremony
}

func newCeremonies() *ceremonies { return &ceremonies{m: map[string]*ceremony{}} }

var errTooManyCeremonies = errf(apitypes.CodeRateLimited, "too many sign-ins in progress; try again in a few minutes")

// put stores cer and returns its ID. One pass over the store (at most
// maxCeremonies) drops the expired ones and what cer replaces: its user's
// live ceremony of the same purpose, or its client's oldest
// unauthenticated one past maxClientCeremonies. A full store drops its
// oldest unauthenticated ceremony, and refuses only when people signed in
// part way fill it.
func (cs *ceremonies) put(cer *ceremony) (string, error) {
	t := now()
	cer.started, cer.expires = t, t.Add(ceremonyTTL)
	if cer.purpose == ceremonySSO {
		cer.expires = t.Add(SSOTTL)
	}
	anon := cer.userID == 0
	if anon {
		cer.client = httpkit.ClientKey(cer.client)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var oldest, oldestOwn string // unauthenticated: anyone's, cer's client's
	own := 0
	for id, x := range cs.m {
		switch {
		case t.After(x.expires), !anon && x.userID == cer.userID && x.purpose == cer.purpose:
			delete(cs.m, id)
		case x.userID == 0:
			if oldest == "" || x.started.Before(cs.m[oldest].started) {
				oldest = id
			}
			if anon && x.client == cer.client {
				own++
				if oldestOwn == "" || x.started.Before(cs.m[oldestOwn].started) {
					oldestOwn = id
				}
			}
		}
	}
	switch {
	case own >= maxClientCeremonies:
		delete(cs.m, oldestOwn)
	case len(cs.m) < maxCeremonies:
	case oldest != "":
		delete(cs.m, oldest)
	default:
		return "", errTooManyCeremonies
	}
	id, _ := newSecret("")
	cs.m[id] = cer
	return id, nil
}

// take removes and returns ceremony id if it is live and for purpose,
// started by user userID in session sessionID.
func (cs *ceremonies) take(id, purpose string, userID, sessionID int64) (*ceremony, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cer := cs.m[id]
	delete(cs.m, id)
	if cer == nil || now().After(cer.expires) || cer.purpose != purpose || cer.userID != userID || cer.sessionID != sessionID {
		return nil, errf(apitypes.CodeExpired, "this %s expired or was already used; start again", ceremonyWords(purpose))
	}
	return cer, nil
}

// retry puts back a ceremony answered wrongly, for another try.
func (cs *ceremonies) retry(id string, cer *ceremony) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.m[id] = cer
}

// ceremonyWords names what a ceremony of purpose is, for its errors.
func ceremonyWords(purpose string) string {
	switch purpose {
	case ceremonyTOTP:
		return "authenticator app setup"
	case ceremonyRegister:
		return "passkey setup"
	}
	return "passkey prompt"
}
