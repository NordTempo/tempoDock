package core

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Random parts are DNS-safe, spread over the whole alphabet at every
// position and as good as distinct: nothing about one predicts another
// (ADR 0087).
//
// 32^7 keys leave 20000 draws a pair alike once in 170 runs, by chance
// alone, so one repeat is no pattern (and failed CI when no repeat was
// allowed); three are, at one run in 30 million.
func TestHostKeysHaveNoPattern(t *testing.T) {
	const n = 20000
	seen := make(map[string]bool, n)
	repeats := 0
	var at [hostKeyLen]map[rune]int
	for i := range at {
		at[i] = map[rune]int{}
	}
	for range n {
		k := newHostKey()
		if len(k) != hostKeyLen || strings.Trim(k, "abcdefghijklmnopqrstuvwxyz234567") != "" {
			t.Fatalf("key %q isn't %d base32 characters", k, hostKeyLen)
		}
		if seen[k] {
			if repeats++; repeats > 2 {
				t.Fatalf("%d keys came again in %d, the last %q", repeats, n, k)
			}
		}
		seen[k] = true
		for i, r := range k {
			at[i][r]++
		}
	}
	// 32 symbols, ~625 each: a fixed or sequential position would show.
	for i, counts := range at {
		if len(counts) != 32 {
			t.Fatalf("position %d uses %d symbols, want all 32", i, len(counts))
		}
		for r, c := range counts {
			if c < n/32/2 || c > n/32*2 {
				t.Errorf("position %d: %q appears %d times of %d", i, r, c, n)
			}
		}
	}
}

// A label keeps its readable prefix where it fits and is cut to 63
// characters with no dash at the end of the cut prefix.
func TestHostLabel(t *testing.T) {
	for _, tc := range []struct{ prefix, want string }{
		{"web-shop-staging", "web-shop-staging-abcdefg"},
		{strings.Repeat("a", 54) + "-bbbb", strings.Repeat("a", 54) + "-abcdefg"},
		{strings.Repeat("x", 80), strings.Repeat("x", 55) + "-abcdefg"},
		{"---", "abcdefg"},
	} {
		if got := hostLabel(tc.prefix, "abcdefg"); got != tc.want || len(got) > 63 {
			t.Errorf("hostLabel(%q) = %q, want %q", tc.prefix, got, tc.want)
		}
	}
}

// Generated hostnames share only their readable prefix: two for the same
// service differ, so knowing one says nothing about the next.
func TestGeneratedHostnamesAreUnguessable(t *testing.T) {
	h := newHarness(t)
	name := strings.Repeat("w", 32)
	web := h.service(name, apitypes.CreateServiceRequest{})
	seen := map[string]bool{}
	for range 5 {
		gen, err := h.c.GenerateDomain(h.ctx, testSystem, web)
		if err != nil {
			t.Fatal(err)
		}
		host := gen.Domain.Hostname
		label := strings.TrimSuffix(host, ".apps.test")
		if seen[host] || len(label) > 63 || !generatedLike(label[:len(label)-hostKeyLen-1], "apps.test").MatchString(host) || !strings.HasPrefix(label, name+"-") {
			t.Fatalf("generated %q (seen before: %v)", host, seen[host])
		}
		seen[host] = true
	}
}
