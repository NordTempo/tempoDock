package core

import (
	"crypto/rand"
	"strings"
)

// Generated hostnames (ADR 0087) are a readable prefix and a random part,
// web-shop-staging-k7m2q9x.<base>, so nobody finds another org's service,
// preview, status page or dashboard by guessing its names. The random
// part is fixed once made: the hostname is stored (domains, sites) or its
// key is (status_pages.address_key).

const (
	// hostKeyLen is the random part's length: 7 base32 characters, 35 bits.
	hostKeyLen = 7
	// hostKeyTries bounds the random parts tried when a hostname is taken,
	// which at 35 bits is all but never.
	hostKeyTries = 8
)

// newHostKey is a random part for a generated hostname: lowercase base32
// (a-z, 2-7), DNS-safe.
func newHostKey() string { return strings.ToLower(rand.Text()[:hostKeyLen]) }

// hostLabel joins prefix and key into one DNS label, cutting prefix so
// the label stays within 63 characters and never has a dash at either end.
func hostLabel(prefix, key string) string {
	prefix = strings.Trim(prefix[:min(len(prefix), 63-1-len(key))], "-")
	if prefix == "" {
		return key
	}
	return prefix + "-" + key
}
