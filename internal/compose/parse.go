package compose

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// lookupFunc finds a variable for interpolation.
type lookupFunc func(name string) (string, bool)

// interpolate expands Compose's variable forms in s: $VAR, ${VAR},
// ${VAR:-default}, ${VAR-default}, ${VAR:?message}, ${VAR?message},
// ${VAR:+alternative}, ${VAR+alternative}, with nesting in defaults, and
// $$ for a literal $. missing names each variable that was needed and
// unset (or empty for the colon forms), with no default to fall back on;
// it expands to "". Anything that isn't a valid form stays as written.
func interpolate(s string, lookup lookupFunc) (out string, missing []string) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' || i+1 == len(s) {
			b.WriteByte(c)
			i++
			continue
		}
		switch next := s[i+1]; {
		case next == '$':
			b.WriteByte('$')
			i += 2
		case next == '{':
			end := closingBrace(s, i+2)
			if end < 0 {
				b.WriteString(s[i:])
				return b.String(), missing
			}
			v, miss, ok := expand(s[i+2:end], lookup)
			if !ok {
				b.WriteString(s[i : end+1])
			} else {
				b.WriteString(v)
				missing = append(missing, miss...)
			}
			i = end + 1
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			name := s[i+1 : j]
			if v, ok := lookup(name); ok {
				b.WriteString(v)
			} else {
				missing = append(missing, name)
			}
			i = j
		default:
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), missing
}

// closingBrace returns the index of the } that closes a ${ whose body
// starts at from, counting nested ${...}, or -1.
func closingBrace(s string, from int) int {
	depth := 1
	for j := from; j < len(s); j++ {
		switch {
		case s[j] == '$' && j+1 < len(s) && s[j+1] == '{':
			depth++
			j++
		case s[j] == '}':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// expand evaluates the body of a ${...} form. ok is false when the body
// isn't a valid form.
func expand(body string, lookup lookupFunc) (string, []string, bool) {
	n := 0
	for n < len(body) && isNameChar(body[n]) {
		n++
	}
	if n == 0 || !isNameStart(body[0]) {
		return "", nil, false
	}
	name, rest := body[:n], body[n:]
	v, set := lookup(name)
	if rest == "" {
		if !set {
			return "", []string{name}, true
		}
		return v, nil, true
	}
	colon := strings.HasPrefix(rest, ":")
	if colon {
		rest = rest[1:]
	}
	if rest == "" {
		return "", nil, false
	}
	op, word := rest[0], rest[1:]
	present := set && (!colon || v != "")
	switch op {
	case '-':
		if present {
			return v, nil, true
		}
		w, miss := interpolate(word, lookup)
		return w, miss, true
	case '?':
		if present {
			return v, nil, true
		}
		return "", []string{name}, true
	case '+':
		if !present {
			return "", nil, true
		}
		w, miss := interpolate(word, lookup)
		return w, miss, true
	}
	return "", nil, false
}

// isNameStart reports whether c can start a variable name.
func isNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isNameChar reports whether c can be part of a variable name.
func isNameChar(c byte) bool { return isNameStart(c) || c >= '0' && c <= '9' }

// envLine is one variable of an env file.
type envLine struct {
	key, value string
	// raw is the value as written, before interpolation.
	raw string
	// bare is a line with a key and no "=": a value from the shell.
	bare    bool
	missing []string
}

// parseEnvFile parses an env file as Compose does: KEY=VALUE lines, #
// comments, an optional `export ` prefix, single quotes (literal, may span
// lines), double quotes (escapes and interpolation, may span lines) and
// unquoted values (interpolation, a " #" starts a comment). Values may use
// the file's earlier variables, then lookup. bad lists the line numbers
// that aren't variables.
func parseEnvFile(content string, lookup lookupFunc) (lines []envLine, bad []int) {
	seen := map[string]string{}
	local := func(name string) (string, bool) {
		if v, ok := seen[name]; ok {
			return v, true
		}
		if lookup != nil {
			return lookup(name)
		}
		return "", false
	}
	rows := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for i := 0; i < len(rows); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(rows[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, hasValue := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			bad = append(bad, lineNo)
			continue
		}
		if !hasValue {
			lines = append(lines, envLine{key: key, bare: true})
			continue
		}
		value = strings.TrimLeft(value, " \t")
		l := envLine{key: key}
		if q := firstByte(value); q == '\'' || q == '"' {
			body := value[1:]
			end := closingQuote(body, q)
			for end < 0 && i+1 < len(rows) {
				i++
				body += "\n" + rows[i]
				end = closingQuote(body, q)
			}
			if end < 0 {
				bad = append(bad, lineNo)
				continue
			}
			l.raw = body[:end]
			if q == '\'' {
				l.value = l.raw
			} else {
				l.value, l.missing = interpolate(unescape(l.raw), local)
			}
		} else {
			if j := strings.Index(value, " #"); j >= 0 {
				value = value[:j]
			}
			l.raw = strings.TrimSpace(value)
			l.value, l.missing = interpolate(l.raw, local)
		}
		seen[key] = l.value
		lines = append(lines, l)
	}
	return lines, bad
}

// firstByte returns s's first byte, or 0.
func firstByte(s string) byte {
	if s == "" {
		return 0
	}
	return s[0]
}

// closingQuote returns the index of the quote q that ends s, skipping
// backslash escapes inside double quotes, or -1.
func closingQuote(s string, q byte) int {
	for j := 0; j < len(s); j++ {
		switch {
		case q == '"' && s[j] == '\\':
			j++
		case s[j] == q:
			return j
		}
	}
	return -1
}

// unescape resolves the escapes of a double-quoted env file value.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for j := 0; j < len(s); j++ {
		if s[j] != '\\' || j+1 == len(s) {
			b.WriteByte(s[j])
			continue
		}
		j++
		switch s[j] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"', '\\':
			b.WriteByte(s[j])
		case '$':
			b.WriteString("$$")
		default:
			b.WriteByte('\\')
			b.WriteByte(s[j])
		}
	}
	return b.String()
}

// portSpec is a parsed port mapping or exposed port.
type portSpec struct {
	// target is the container port; targetEnd the end of a range, else 0.
	target, targetEnd int
	// published is true for a `ports` entry that maps a host port (or
	// lets Docker pick one); false for container-only ports and `expose`.
	published bool
	// hostIP is the host address a published port binds to, if any.
	hostIP   string
	protocol string
	// text is the entry as written, for warnings.
	text string
}

// parsePort parses a short `ports` entry: [[HOST_IP:]HOST_PORT:]PORT[/PROTO],
// where the ports may be ranges and an IPv6 host is in brackets.
func parsePort(s string) (portSpec, error) {
	p := portSpec{text: s, protocol: "tcp"}
	rest := s
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		p.protocol, rest = strings.ToLower(rest[i+1:]), rest[:i]
	}
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]:")
		if end < 0 {
			return p, fmt.Errorf("port %q has an unterminated IPv6 address", s)
		}
		p.hostIP, rest = rest[1:end], rest[end+2:]
		if !strings.Contains(rest, ":") {
			return p, fmt.Errorf("port %q has a host address but no container port", s)
		}
	}
	parts := strings.Split(rest, ":")
	switch len(parts) {
	case 1:
	case 2:
		p.published = true
	case 3:
		p.published = true
		p.hostIP = parts[0]
	default:
		return p, fmt.Errorf("port %q isn't [HOST_IP:]HOST_PORT:CONTAINER_PORT", s)
	}
	var err error
	p.target, p.targetEnd, err = parsePortRange(parts[len(parts)-1])
	if err != nil {
		return p, fmt.Errorf("port %q: %w", s, err)
	}
	return p, nil
}

// parseExpose parses an `expose` entry: PORT[-END][/PROTO].
func parseExpose(s string) (portSpec, error) {
	p := portSpec{text: s, protocol: "tcp"}
	rest := s
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		p.protocol, rest = strings.ToLower(rest[i+1:]), rest[:i]
	}
	var err error
	p.target, p.targetEnd, err = parsePortRange(rest)
	if err != nil {
		return p, fmt.Errorf("exposed port %q: %w", s, err)
	}
	return p, nil
}

// parsePortRange parses "80" or "8000-8010"; end is 0 for a single port.
func parsePortRange(s string) (start, end int, err error) {
	a, b, isRange := strings.Cut(s, "-")
	if start, err = parsePortNumber(a); err != nil {
		return 0, 0, err
	}
	if isRange {
		if end, err = parsePortNumber(b); err != nil {
			return 0, 0, err
		}
		if end < start {
			return 0, 0, fmt.Errorf("range %s ends before it starts", s)
		}
	}
	return start, end, nil
}

// parsePortNumber parses a port number, 1-65535.
func parsePortNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%q isn't a port number (1-65535)", s)
	}
	return n, nil
}

// parseMemory parses a Compose memory size ("512m", "1g", "1.5GB", "512Mi",
// or plain bytes) into whole megabytes (MiB), rounded up.
func parseMemory(s string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	i := 0
	for i < len(t) && (t[i] >= '0' && t[i] <= '9' || t[i] == '.') {
		i++
	}
	n, err := strconv.ParseFloat(t[:i], 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("memory %q isn't a size like 512m or 1g", s)
	}
	unit := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(t[i:]), "b"), "i")
	shift := map[string]int{"": 0, "k": 10, "m": 20, "g": 30, "t": 40}
	sh, ok := shift[unit]
	if !ok {
		return 0, fmt.Errorf("memory %q has an unknown unit; use b, k, m or g", s)
	}
	mb := math.Ceil(n * math.Exp2(float64(sh-20)))
	if mb > math.MaxInt32 {
		return 0, fmt.Errorf("memory %q is too large", s)
	}
	return int(mb), nil
}

// shellQuote quotes a word for sh when it needs it.
func shellQuote(w string) string {
	if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// shellJoin joins words into a command line sh splits back into them.
func shellJoin(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = shellQuote(w)
	}
	return strings.Join(quoted, " ")
}

// shellWords splits the first simple command of a shell line into words:
// quotes and backslashes are resolved, and it stops at the first ;, |, &
// or newline outside quotes. Good enough to recognise a health check, not
// a shell.
func shellWords(line string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\'' || c == '"':
			end := strings.IndexByte(line[i+1:], c)
			if end < 0 {
				end = len(line) - i - 1
			}
			cur.WriteString(line[i+1 : i+1+end])
			inWord = true
			i += end + 1
		case c == '\\' && i+1 < len(line):
			cur.WriteByte(line[i+1])
			inWord = true
			i++
		case c == ';' || c == '|' || c == '&' || c == '\n':
			flush()
			return words
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flush()
	return words
}

// urlParts is a URL split without re-encoding anything, so a rewrite
// keeps the rest of it exactly as written.
type urlParts struct {
	scheme, user, password string
	hasPassword            bool
	host, port             string
	// rest is everything after the authority: path, query and fragment.
	rest string
}

// splitURL splits scheme://[user[:password]@]host[:port][rest].
func splitURL(s string) (urlParts, bool) {
	var u urlParts
	scheme, after, ok := strings.Cut(s, "://")
	if !ok || scheme == "" || !isNameStart(scheme[0]) ||
		strings.Trim(strings.ToLower(scheme), "abcdefghijklmnopqrstuvwxyz0123456789+.-") != "" {
		return u, false
	}
	u.scheme = strings.ToLower(scheme)
	authority := after
	if i := strings.IndexAny(after, "/?#"); i >= 0 {
		authority, u.rest = after[:i], after[i:]
	}
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		userinfo := authority[:i]
		authority = authority[i+1:]
		u.user, u.password, u.hasPassword = strings.Cut(userinfo, ":")
	}
	u.host = authority
	if strings.HasPrefix(authority, "[") {
		if end := strings.Index(authority, "]"); end >= 0 {
			u.host = authority[:end+1]
			u.port = strings.TrimPrefix(authority[end+1:], ":")
		}
	} else if h, p, ok := strings.Cut(authority, ":"); ok {
		u.host, u.port = h, p
	}
	return u, u.host != ""
}

// imageRef is an image reference split into repository and tag.
type imageRef struct {
	// repo is the repository with Docker Hub's default registry and
	// library/ prefix removed: "postgres", "bitnami/redis".
	repo string
	tag  string
}

// parseImage splits an image reference, dropping any digest.
func parseImage(s string) imageRef {
	s, _, _ = strings.Cut(strings.ToLower(s), "@")
	var r imageRef
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		s, r.tag = s[:i], s[i+1:]
	}
	for _, prefix := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		s = strings.TrimPrefix(s, prefix)
	}
	r.repo = strings.TrimPrefix(s, "library/")
	return r
}

// name is the repository's last path segment: "redis" for bitnami/redis.
func (r imageRef) name() string { return r.repo[strings.LastIndex(r.repo, "/")+1:] }

// major is the tag's leading version number ("16" for 16.2-alpine), or "".
func (r imageRef) major() string {
	i := 0
	for i < len(r.tag) && r.tag[i] >= '0' && r.tag[i] <= '9' {
		i++
	}
	return r.tag[:i]
}
