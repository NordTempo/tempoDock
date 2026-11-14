package httpkit

import (
	"strings"
	"time"
)

// LoginLimiter throttles password attempts without letting a guesser lock
// the account's owner out. An attempt spends from three buckets, in order,
// and stops at the first that is empty:
//
//   - the client IP (20 at once, then one per 3s), loose enough for people
//     sharing a NAT;
//   - the (email, IP) pair (5 at once, then one per 10s), which is what
//     stops a single guesser;
//   - the email alone (30 at once, then one per 2s), a backstop against
//     guessing from many addresses.
//
// A guesser on one IP is stopped at its pair and never drains the email's
// bucket, so its owner can still sign in from their own address; locking
// them out takes more than six addresses guessing at full rate.
type LoginLimiter struct {
	ip, pair, email *Limiter
}

// NewLoginLimiter returns a LoginLimiter with the limits above.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		ip:    NewLimiter(20, 3*time.Second),
		pair:  NewLimiter(5, 10*time.Second),
		email: NewLimiter(30, 2*time.Second),
	}
}

// Allow spends an attempt to sign in as email (any case) from ip at t,
// reporting false when it must wait. IPs count by ClientKey.
func (l *LoginLimiter) Allow(email, ip string, t time.Time) bool {
	email, ip = strings.ToLower(email), ClientKey(ip)
	return l.ip.Allow(ip, t) && l.pair.Allow(email+"\x00"+ip, t) && l.email.Allow(email, t)
}

// AllowIP spends an attempt from ip that names no account (a setup code,
// a sign-in link), reporting false when it must wait.
func (l *LoginLimiter) AllowIP(ip string, t time.Time) bool {
	return l.ip.Allow(ClientKey(ip), t)
}
