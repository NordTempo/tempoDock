package inject

import "strings"

// cspNonce reads a response's Content-Security-Policy headers and reports
// whether a script from the page's own host may run, and with which nonce.
// Each policy is judged by script-src-elem, else script-src, else
// default-src: no such directive allows it; 'self', * or a scheme
// (https:) allow it unless 'strict-dynamic' is there too; otherwise only
// a nonce does. The first nonce found is the one used, so every policy
// must accept the script with it.
func cspNonce(headers []string) (nonce string, allowed bool) {
	var policies [][]string // each policy's script directive's sources; nil for none
	for _, h := range headers {
		for p := range strings.SplitSeq(h, ",") {
			src := scriptSources(p)
			policies = append(policies, src)
			if nonce == "" {
				nonce = firstNonce(src)
			}
		}
	}
	for _, src := range policies {
		if src != nil && !allows(src, nonce) {
			return "", false
		}
	}
	return nonce, true
}

// scriptSources is the source list that governs scripts in one policy, or
// nil when it has none of the script directives.
func scriptSources(policy string) []string {
	var found map[string][]string
	for d := range strings.SplitSeq(policy, ";") {
		fields := strings.Fields(d)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		switch name {
		case "script-src-elem", "script-src", "default-src":
			if found == nil {
				found = map[string][]string{}
			}
			if _, dup := found[name]; !dup { // the first of a name counts
				found[name] = append([]string{}, fields[1:]...)
			}
		}
	}
	for _, name := range []string{"script-src-elem", "script-src", "default-src"} {
		if src, ok := found[name]; ok {
			return src
		}
	}
	return nil
}

// allows reports whether sources let a script from the page's host run,
// carrying nonce.
func allows(sources []string, nonce string) bool {
	var host, strict bool
	for _, s := range sources {
		switch ls := strings.ToLower(s); {
		case ls == "'strict-dynamic'":
			strict = true
		case ls == "'self'" || ls == "*" || ls == "https:" || ls == "http:":
			host = true
		case nonce != "" && s == "'nonce-"+nonce+"'":
			return true
		}
	}
	return host && !strict
}

// firstNonce is the value of the first 'nonce-...' source with nothing in
// it that couldn't go into an HTML attribute as it is.
func firstNonce(sources []string) string {
	for _, s := range sources {
		v, ok := strings.CutPrefix(s, "'nonce-")
		if !ok || !strings.HasSuffix(v, "'") {
			continue
		}
		v = strings.TrimSuffix(v, "'")
		if v != "" && strings.Trim(v, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=_-") == "" {
			return v
		}
	}
	return ""
}
