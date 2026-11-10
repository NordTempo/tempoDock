package data

// KeyMatch reports whether key matches the Redis glob pattern, with
// Redis' rules: * any run, ? one byte, [abc], [^a] and [a-z] classes,
// and \ quoting the next byte. Sensitive key patterns use it, so a
// pattern masks exactly the keys SCAN MATCH with it finds.
func KeyMatch(pattern, key string) bool {
	p, k := 0, 0
	// The last * and the key position it was tried at, for backtracking.
	star, mark := -1, 0
	for k < len(key) {
		if p < len(pattern) {
			switch c := pattern[p]; c {
			case '*':
				star, mark = p, k
				p++
				continue
			case '?':
				p, k = p+1, k+1
				continue
			case '[':
				if end, ok := matchClass(pattern, p, key[k]); end > 0 {
					if ok {
						p, k = end, k+1
						continue
					}
				} else if key[k] == '[' { // an unclosed [ is literal
					p, k = p+1, k+1
					continue
				}
			case '\\':
				if p+1 < len(pattern) && pattern[p+1] == key[k] {
					p, k = p+2, k+1
					continue
				}
			default:
				if c == key[k] {
					p, k = p+1, k+1
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		p, mark = star+1, mark+1
		k = mark
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchClass matches b against the class starting at pattern[p] ('['):
// the index after its ']' (0 when it never closes) and whether b is in
// it.
func matchClass(pattern string, p int, b byte) (int, bool) {
	i := p + 1
	negate := i < len(pattern) && pattern[i] == '^'
	if negate {
		i++
	}
	found := false
	for first := true; i < len(pattern); first = false {
		c := pattern[i]
		if c == ']' && !first {
			return i + 1, found != negate
		}
		if c == '\\' && i+1 < len(pattern) {
			i++
			c = pattern[i]
		}
		if i+2 < len(pattern) && pattern[i+1] == '-' && pattern[i+2] != ']' {
			lo, hi := c, pattern[i+2]
			if lo > hi {
				lo, hi = hi, lo
			}
			found = found || b >= lo && b <= hi
			i += 3
			continue
		}
		found = found || b == c
		i++
	}
	return 0, false
}

// KeySensitive reports whether key matches any of patterns, and which.
func KeySensitive(patterns []string, key string) (string, bool) {
	for _, p := range patterns {
		if KeyMatch(p, key) {
			return p, true
		}
	}
	return "", false
}
