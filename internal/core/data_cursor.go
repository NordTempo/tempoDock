package core

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// Page cursors. The engine's cursors hold the last row's key values in the
// clear, sensitive ones included when a table's key is sensitive, and only
// an unkeyed check of the request. Callers get them sealed with the secrets
// box instead (AES-GCM: encrypted and authenticated), bound to the
// database and the caller, so a cursor can't be read, forged (as an oracle
// for key values) or replayed by someone else.

// errBadCursor is the answer to any cursor that doesn't open.
var errBadCursor = invalid("the cursor isn't valid for this table and caller; start from the first page")

// cursorContext binds a sealed cursor to a database and an actor.
func cursorContext(a Actor, serviceID int64) string {
	return strconv.FormatInt(serviceID, 10) + "\x00" + actorKey(a) + "\x00"
}

// sealCursor turns an engine cursor ("" for none) into the one a caller
// sees.
func (c *Core) sealCursor(a Actor, serviceID int64, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	b, err := c.box.Seal([]byte(cursorContext(a, serviceID) + raw))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// openCursor reverses sealCursor for the same database and actor; any
// other cursor is errBadCursor.
func (c *Core) openCursor(a Actor, serviceID int64, sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", errBadCursor
	}
	if b, err = c.box.Open(b); err != nil {
		return "", errBadCursor
	}
	raw, ok := strings.CutPrefix(string(b), cursorContext(a, serviceID))
	if !ok {
		return "", errBadCursor
	}
	return raw, nil
}
