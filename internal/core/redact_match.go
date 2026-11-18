package core

import (
	"bytes"
	"slices"
)

// matcher finds any of a set of strings in text in one pass: an
// Aho-Corasick automaton, so what a byte of text costs doesn't grow with
// the number of strings. It is a trie of the strings with, for each node,
// where to go on when the text stops following it (fail). Nodes are
// numbered breadth first, which puts a node's children next to each
// other: the automaton is five flat arrays, about 17 bytes per byte of
// the strings, however many there are. It is read-only once built.
type matcher struct {
	// root is the root's child for each byte, 0 for none (the root is
	// node 0 and nobody's child).
	root [256]int32
	// pairs has a bit for every two bytes a string begins with: the
	// root skips text that can't begin one without walking the trie.
	pairs [1 << 13]byte
	// first[n] to first[n+1]-1 are node n's children, and label[c] is
	// the byte that leads to child c.
	first []int32
	label []byte
	// fail[n] is the node of the longest proper suffix of n's string
	// that the trie holds.
	fail []int32
	// depth[n] is the length of n's string.
	depth []int32
	// ends[n] is the length of the longest of the strings that n's
	// string ends with, 0 for none.
	ends []int32
}

// newMatcher builds the matcher of pats, which are distinct and not
// empty. It sorts pats.
func newMatcher(pats []string) *matcher {
	slices.Sort(pats)
	total := 1
	for _, p := range pats {
		total += len(p)
	}
	m := &matcher{
		first: make([]int32, 0, total+1),
		label: make([]byte, 1, total),
		depth: make([]int32, 1, total),
		ends:  make([]int32, 1, total),
	}
	// The trie, level by level: a node is the run of sorted strings that
	// share its string as a prefix, and its children split that run by
	// the next byte. The string that ends at the node sorts first.
	type run struct{ lo, hi, parent int32 }
	runs := make([]run, 1, total)
	runs[0] = run{0, int32(len(pats)), 0}
	for n := 0; n < len(runs); n++ {
		r, d := runs[n], m.depth[n]
		m.first = append(m.first, int32(len(runs)))
		lo := r.lo
		if len(pats[lo]) == int(d) {
			m.ends[n] = d
			lo++
		}
		for lo < r.hi {
			c, hi := pats[lo][d], lo+1
			for hi < r.hi && pats[hi][d] == c {
				hi++
			}
			runs = append(runs, run{lo, hi, int32(n)})
			m.label = append(m.label, c)
			m.depth = append(m.depth, d+1)
			m.ends = append(m.ends, 0)
			lo = hi
		}
	}
	m.first = append(m.first, int32(len(runs)))

	// Fail links, in the same order: a node's is found from its
	// parent's, which is shallower and so already there.
	m.fail = make([]int32, len(runs))
	for n := 1; n < len(runs); n++ {
		if p := runs[n].parent; p == 0 {
			m.root[m.label[n]] = int32(n)
		} else {
			m.fail[n] = m.step(m.fail[p], m.label[n])
		}
		if m.ends[n] == 0 {
			m.ends[n] = m.ends[m.fail[n]]
		}
	}
	for _, p := range pats {
		if len(p) == 1 { // begins and ends with whatever follows
			for i := uint(p[0]) << 8; i < uint(p[0])<<8+256; i += 8 {
				m.pairs[i>>3] = 0xff
			}
			continue
		}
		i := uint(p[0])<<8 | uint(p[1])
		m.pairs[i>>3] |= 1 << (i & 7)
	}
	return m
}

// step is the node after s once the text goes on with c: s's child for
// c, or that of the nearest node on s's fail chain that has one. Each
// step up the chain undoes an earlier step down, so a text costs two
// steps a byte at most, whatever the strings are.
func (m *matcher) step(s int32, c byte) int32 {
	for s != 0 {
		lo, hi := m.first[s], m.first[s+1]
		if hi-lo <= 4 { // nearly every node: a string's own bytes
			for i := lo; i < hi; i++ {
				if m.label[i] == c {
					return i
				}
			}
		} else if i := bytes.IndexByte(m.label[lo:hi], c); i >= 0 {
			return lo + int32(i)
		}
		s = m.fail[s]
	}
	return m.root[c]
}

// tail is the length of the longest end of text that is the start, and
// not all, of one of the strings.
func (m *matcher) tail(text []byte) int {
	var s int32
	for _, c := range text {
		s = m.step(s, c)
	}
	for s != 0 && m.first[s] == m.first[s+1] {
		s = m.fail[s]
	}
	return int(m.depth[s])
}

// found is one of the strings in a text: where it starts and its length.
type found struct{ at, n int }

// findBackwards appends to out the longest string that starts at each
// place in src where one does, last place first, for a matcher of the
// strings reversed: reading src backwards, a reversed string ends where
// the string itself starts, and ends holds the longest that does.
func findBackwards[T string | []byte](m *matcher, src T, out []found) []found {
	var s int32
	for i := len(src) - 1; i >= 0; i-- {
		c := src[i]
		if s == 0 {
			// Nothing begun: skip to where two bytes begin a string.
			for i > 0 {
				p := uint(c)<<8 | uint(src[i-1])
				if m.pairs[p>>3]&(1<<(p&7)) != 0 {
					break
				}
				i--
				c = src[i]
			}
			if s = m.root[c]; s == 0 {
				continue
			}
		} else {
			s = m.step(s, c)
		}
		if n := m.ends[s]; n != 0 {
			out = append(out, found{i, int(n)})
		}
	}
	return out
}
