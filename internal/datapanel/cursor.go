package datapanel

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/secrets"
)

// Page cursors. The engine's cursors hold the last row's key values in the
// clear, sensitive ones included when a table's key is sensitive, and only
// an unkeyed check of the request. Callers get them sealed with the secrets
// box instead (AES-GCM: encrypted and authenticated), bound to the
// database and the caller, so a cursor can't be read, forged (as an oracle
// for key values) or replayed by someone else.

// errBadCursor is the answer to any cursor that doesn't open.
var errBadCursor = Errorf(apitypes.CodeValidation, "the cursor isn't valid for this table and caller; start from the first page")

// cursorAAD is a cursor's AAD. A cursor isn't stored; cursorContext, in
// the plaintext, binds it to the database and the caller.
var cursorAAD = secrets.Bind("datapanel.cursor")

// cursorContext binds a sealed cursor to a database and an actor.
func cursorContext(a Actor, database int64) string {
	return strconv.FormatInt(database, 10) + "\x00" + ActorKey(a) + "\x00"
}

// sealCursor turns an engine cursor ("" for none) into the one a caller
// sees.
func (p *Panel[D]) sealCursor(a Actor, database int64, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	b, err := p.box.Seal([]byte(cursorContext(a, database)+raw), cursorAAD)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// openCursor reverses sealCursor for the same database and actor; any
// other cursor is errBadCursor.
func (p *Panel[D]) openCursor(a Actor, database int64, sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", errBadCursor
	}
	if b, err = p.box.Open(b, cursorAAD); err != nil {
		return "", errBadCursor
	}
	raw, ok := strings.CutPrefix(string(b), cursorContext(a, database))
	if !ok {
		return "", errBadCursor
	}
	return raw, nil
}
