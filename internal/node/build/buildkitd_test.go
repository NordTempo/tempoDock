package build

import (
	"strings"
	"testing"
)

func TestCgroupProblem(t *testing.T) {
	const mem = 512 << 20
	cases := []struct {
		out  string
		want string // substring; "" means no problem
	}{
		{"536870912\n0\n", ""},
		{"536870912\nmax\n", "memory.swap.max is max"},
		{"max\n0\n", "memory.max is max"},
		{"unknown\nunknown\n", ""},   // cgroup v1: can't tell
		{"536870912\nunknown\n", ""}, // swap accounting off
		{"", ""},                     // exec failed
	}
	for _, tc := range cases {
		got := cgroupProblem(tc.out, mem)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("cgroupProblem(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}
