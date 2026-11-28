package wire

import (
	"errors"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/bidi"
	"golang.org/x/text/unicode/norm"
)

// saslPrep prepares a password for SCRAM as RFC 4013 says, as MongoDB's
// server does: non-ASCII spaces become a space, characters mapped to
// nothing go, the rest is NFKC, control characters are refused and so is
// mixed-direction text (RFC 3454 section 6). Printable ASCII, every
// password Dock generates, is left as it is. One case is not followed:
// Go's NFKC adds a grapheme joiner after 30 combining marks in a row,
// which no real password holds.
func saslPrep(s string) (string, error) {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			ascii = false
			break
		}
	}
	if ascii {
		return s, nil
	}
	mapped := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			return "", errors.New("the password isn't valid UTF-8")
		case r == 0x00A0, r == 0x1680, r >= 0x2000 && r <= 0x200B, r == 0x202F, r == 0x205F, r == 0x3000:
			mapped = append(mapped, ' ') // RFC 3454 C.1.2
		case r == 0x00AD, r == 0x034F, r == 0x1806, r >= 0x180B && r <= 0x180D, r >= 0x200C && r <= 0x200D,
			r == 0x2060, r >= 0xFE00 && r <= 0xFE0F, r == 0xFEFF:
			// RFC 3454 B.1: mapped to nothing.
		default:
			mapped = append(mapped, r)
		}
	}
	out := norm.NFKC.String(string(mapped))
	for _, r := range out {
		if unicode.IsControl(r) || (r >= 0xE000 && r <= 0xF8FF) || (r >= 0xFDD0 && r <= 0xFDEF) {
			return "", errors.New("the password holds a character SCRAM doesn't allow")
		}
	}
	if out == "" {
		return "", errors.New("the password is empty after SASLprep")
	}
	if !bidiOK(out) {
		return "", errors.New("the password mixes right-to-left and left-to-right text in a way SASLprep refuses")
	}
	return out, nil
}

// bidiOK is RFC 3454's bidirectional rule: a string with right-to-left
// characters holds no left-to-right ones and starts and ends with
// right-to-left ones.
func bidiOK(s string) bool {
	rtl := func(r rune) bool {
		p, _ := bidi.LookupRune(r)
		return p.Class() == bidi.R || p.Class() == bidi.AL
	}
	runes := []rune(s)
	hasRTL := false
	for _, r := range runes {
		if rtl(r) {
			hasRTL = true
			break
		}
	}
	if !hasRTL {
		return true
	}
	for _, r := range runes {
		if p, _ := bidi.LookupRune(r); p.Class() == bidi.L {
			return false
		}
	}
	return rtl(runes[0]) && rtl(runes[len(runes)-1])
}
