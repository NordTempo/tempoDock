package core

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238's default, the one every authenticator app supports
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Authenticator app codes (TOTP, RFC 6238) with the parameters every app
// supports: HMAC-SHA1, 6 digits, 30-second steps. A code is accepted for
// its step and one step either side (clock drift), and each step works
// once per factor (user_factors.last_step).
const (
	totpPeriod = 30
	totpDigits = 6
	// totpDrift is how many steps either side of now are accepted.
	totpDrift = 1
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPKey returns a random 160-bit key, RFC 4226's recommended size.
func newTOTPKey() []byte {
	k := make([]byte, 20)
	_, _ = rand.Read(k)
	return k
}

// totpStep is the time step t falls in.
func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// totpCode is the code for key at time step step.
func totpCode(key []byte, step int64) string {
	mac := hmac.New(sha1.New, key)
	_ = binary.Write(mac, binary.BigEndian, step)
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, n%1_000_000)
}

// matchTOTP returns the time step code is valid for at t, within the
// drift, and false when it is valid for none. Codes are compared in
// constant time.
func matchTOTP(key []byte, code string, t time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	now := totpStep(t)
	for d := int64(-totpDrift); d <= totpDrift; d++ {
		if subtle.ConstantTimeCompare([]byte(totpCode(key, now+d)), []byte(code)) == 1 {
			return now + d, true
		}
	}
	return 0, false
}

// totpURI is the otpauth:// URI an authenticator app scans, labelled with
// the issuer (Dock's host) and the user's email.
func totpURI(key []byte, issuer, email string) string {
	q := url.Values{
		"secret": {totpEncoding.EncodeToString(key)}, "issuer": {issuer},
		"algorithm": {"SHA1"}, "digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpPeriod)},
	}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+email) + "?" + q.Encode()
}
