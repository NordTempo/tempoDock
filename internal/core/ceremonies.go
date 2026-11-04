package core

import (
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Ceremonies are sign-in steps in flight that hold a secret until the
// person answers (ADR 0048): an authenticator app's new key waiting for
// its first code. They live in memory for ceremonyTTL, bound to the user
// and session that started them, and are taken out when answered, so an
// answer works once. A restart forgets them; the person starts again.

const (
	ceremonyTTL = 5 * time.Minute
	// maxCeremonies bounds how many can be held in memory.
	maxCeremonies = 1000
)

// Ceremony purposes: what a ceremony may be answered for.
const (
	ceremonyTOTP = "totp" // an authenticator app awaiting its first code
)

// ceremony is a step in flight, bound to the user and session that
// started it.
type ceremony struct {
	purpose   string
	userID    int64
	sessionID int64
	totpKey   []byte
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
		return nil, errf(apitypes.CodeExpired, "this step expired or was already used; start again")
	}
	return cer, nil
}

// retry puts back a ceremony answered wrongly, for another try.
func (cs *ceremonies) retry(id string, cer *ceremony) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.m[id] = cer
}
