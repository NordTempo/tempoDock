package core

import (
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Ceremonies are sign-in steps in flight that hold a secret until the
// person answers (ADR 0057): a passkey challenge, an authenticator app's
// new key waiting for its first code, or a single sign-on waiting for the
// identity provider to send the browser back (ADR 0058). They live in
// memory for ceremonyTTL (SSOTTL), bound to the user and session that
// started them (a single sign-on to its browser instead), and are taken
// out when answered, so an answer works once. A restart forgets them; the
// person starts again.

const (
	ceremonyTTL = 5 * time.Minute
	// SSOTTL is how long a person has at their identity provider; the
	// API's browser binding cookie lasts as long.
	SSOTTL = 10 * time.Minute
	// maxCeremonies bounds how many can be held in memory.
	maxCeremonies = 1000
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
// passkeys, the origin it was asked from.
type ceremony struct {
	purpose   string
	userID    int64
	sessionID int64
	origin    string
	webauthn  webauthn.SessionData
	totpKey   []byte
	sso       *ssoFlow
	attempts  int
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

// put stores cer and returns its ID.
func (cs *ceremonies) put(cer *ceremony) (string, error) {
	t := now()
	cer.expires = t.Add(ceremonyTTL)
	if cer.purpose == ceremonySSO {
		cer.expires = t.Add(SSOTTL)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.m) >= maxCeremonies {
		for id, x := range cs.m {
			if t.After(x.expires) {
				delete(cs.m, id)
			}
		}
		if len(cs.m) >= maxCeremonies {
			return "", errTooManyCeremonies
		}
	}
	// One live ceremony per user and purpose: starting again replaces it,
	// so nobody fills the store alone.
	if cer.userID != 0 {
		for id, x := range cs.m {
			if x.userID == cer.userID && x.purpose == cer.purpose {
				delete(cs.m, id)
			}
		}
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
