package compose

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// nameRe is Dock's rule for project, service and volume names (core's
// validName): DNS-safe, because service names are private hostnames.
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// keyRe is Dock's rule for variable names (core's validKey).
var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedNames can't be service names: references use them as scopes
// (${{shared.KEY}}, ${{org.KEY}}).
var reservedNames = []string{"shared", "org"}

// maxName is the longest Dock name.
const maxName = 32

// normalizeName turns a Compose name into a Dock name candidate: lower
// case, runs of anything but letters and digits as one dash, trimmed to
// 32 characters. It returns fallback when nothing is left.
func normalizeName(s, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	out := b.String()
	if len(out) > maxName {
		out = strings.TrimRight(out[:maxName], "-")
	}
	if out == "" {
		return fallback
	}
	return out
}

// assignNames gives each name a unique, valid Dock name not in reserved.
// Names that are valid already keep them; the others are normalized and,
// on a collision, numbered (-2, -3, ...), in the order given.
func assignNames(names []string, fallback string, reserved []string) map[string]string {
	out := make(map[string]string, len(names))
	taken := map[string]bool{}
	for _, r := range reserved {
		taken[r] = true
	}
	for _, n := range names {
		if nameRe.MatchString(n) && !taken[n] {
			out[n] = n
			taken[n] = true
		}
	}
	for _, n := range names {
		if _, ok := out[n]; ok {
			continue
		}
		base := normalizeName(n, fallback)
		name := base
		for i := 2; taken[name]; i++ {
			suffix := "-" + strconv.Itoa(i)
			name = strings.TrimRight(base[:min(len(base), maxName-len(suffix))], "-") + suffix
		}
		out[n] = name
		taken[name] = true
	}
	return out
}

// validKey reports why a variable name can't be used in Dock, or "".
func validKey(k string) string {
	switch {
	case !keyRe.MatchString(k):
		return "isn't a valid variable name in Dock (letters, digits and underscores, not starting with a digit), so it is skipped. Rename it in the app and set it with `dock env set`."
	case k == "PORT":
		return "is set by Dock from the service's port setting, so this value isn't used."
	case strings.HasPrefix(k, "DOCK_"):
		return "uses the DOCK_ prefix, which is reserved for variables Dock injects, so it is skipped. Rename it in the app and set it with `dock env set`."
	}
	return ""
}

// secretWords are the parts of a variable name that make it a secret.
var secretWords = []string{"PASSWORD", "PASSWD", "PASS", "SECRET", "TOKEN", "API_KEY", "APIKEY", "PRIVATE", "CREDENTIAL", "SALT", "AUTH"}

// addressSuffixes mark variables that hold an address (NEXTAUTH_URL,
// AUTH_HOST): secret only by their value, a URL with a password.
var addressSuffixes = []string{"_URL", "_URI", "_HOST", "_ENDPOINT"}

// isSecretKey reports whether a variable name looks like it holds a secret.
func isSecretKey(k string) bool {
	k = strings.ToUpper(k)
	if k == "KEY" || strings.HasSuffix(k, "_KEY") {
		return true
	}
	if slices.ContainsFunc(addressSuffixes, func(s string) bool { return strings.HasSuffix(k, s) }) {
		return false
	}
	return slices.ContainsFunc(secretWords, func(w string) bool { return strings.Contains(k, w) })
}

// isSecretValue reports whether a value holds a secret Dock can see: a
// URL with a password that isn't a reference.
func isSecretValue(v string) bool {
	u, ok := splitURL(v)
	return ok && u.password != "" && !refOnlyRe.MatchString(u.password)
}
