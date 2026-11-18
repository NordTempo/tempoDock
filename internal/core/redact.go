package core

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Secret values in logs (docs/safety-model.md). Every variable value is
// secret: only admins may reveal them (ListVariables), so build and
// container logs show them masked to everyone else. The values are the
// ones the release's container and build got (its config's environment,
// with references to shared and org variables resolved), so an old
// release's logs are masked with the values it ran with. Masking is
// best-effort against accidental exposure, not against an app that
// encodes or splits a value before printing it.

// secretMask replaces a secret value in a log.
const secretMask = "*****"

// minSecretLen is the shortest value masked. Shorter values (true, debug,
// 8080) are rarely secrets and common words in logs, so masking them would
// shred the logs. It is longer than secretMask, so masking never makes a
// log longer.
const minSecretLen = 6

// revealsAt reports whether a may see secret values in an environment
// (ADR 0080: variables.read_secret where they are). envID 0 asks for every
// environment of the project, projectID 0 for every one of the org (the
// org's variables reach them all); a grant somewhere else doesn't count.
// Actors grants don't decide reveal as admins did before, inside their
// token's limit.
func (a Actor) revealsAt(projectID, envID int64, kind uint8) bool {
	if !a.hasGrants {
		return a.require(apitypes.ScopeAdmin, "revealing secret values") == nil && a.limitCovers(projectID, envID)
	}
	for _, g := range a.grants {
		if !g.perms.has(PermVariablesReadSecret) {
			continue
		}
		every := g.kinds == uint8(allKinds)
		switch {
		case projectID == 0:
			if g.project == 0 && g.env == 0 && every {
				return true
			}
		case envID == 0:
			if (g.project == 0 || g.project == projectID) && g.env == 0 && every {
				return true
			}
		case modelAllows(g, PermVariablesReadSecret, projectID, envID, kind):
			return true
		}
	}
	return false
}

// revealsIn is revealsAt for an environment.
func (a Actor) revealsIn(es envScope) bool {
	return a.revealsAt(es.project.ID, es.env.ID, envKind(es.env))
}

// requireReveal checks a may reveal secret values at a place (revealsAt;
// es zero for the whole org) after a recent sign-in (ADR 0057).
func (a Actor) requireReveal(es envScope) error {
	if err := a.mayReveal(es, "revealing secret values"); err != nil {
		return err
	}
	return a.requireRecent("revealing secret values")
}

// mayReveal is requireReveal without the recent sign-in, for action.
func (a Actor) mayReveal(es envScope, action string) error {
	ok := a.revealsAt(0, 0, 0)
	if es.env.ID != 0 {
		ok = a.revealsIn(es)
	}
	switch {
	case ok:
		return nil
	case !a.hasGrants && es.env.ID != 0:
		return a.require(apitypes.ScopeAdmin, action)
	case !a.hasGrants && a.limited():
		// The org's values reach every environment: a limited token
		// sees none of them.
		return a.refuseLimit(apitypes.ScopeAdmin, action, "", fmt.Sprintf("%s is limited to %s; %s of the organization requires an unlimited admin token", a, a.limitName(), action))
	case !a.hasGrants:
		return a.require(apitypes.ScopeAdmin, action)
	case es.env.ID != 0:
		return a.refuseGrant(apitypes.ScopeAdmin, "revealing secret values", es.String())
	}
	return a.refuseGrant(apitypes.ScopeAdmin, "revealing secret values", a.orgName())
}

// logRedactor returns what masks rel's secret values in its logs for a in
// es, rel's environment: nil when a may reveal them there or there are
// none, so they get logs as written at no cost. It reads no more than
// the release row it is given.
func (c *Core) logRedactor(a Actor, es envScope, rel store.Release) (*redactor, error) {
	if a.revealsIn(es) {
		return nil, nil
	}
	return c.releaseRedactor(rel)
}

// releaseRedactor masks rel's secret values whoever reads: what an actor
// who can't reveal them gets, and what log drains always apply.
func (c *Core) releaseRedactor(rel store.Release) (*redactor, error) {
	cfg, err := c.openConfig(rel)
	if err != nil {
		return nil, err
	}
	return newRedactor(cfg.Env), nil
}

// redactor masks a fixed set of secret values in log text. A nil
// redactor masks nothing. The values are found with one automaton
// (matcher), built once: a byte of text costs the same whether a service
// has ten values or ten thousand, and a line without secrets costs one
// pass and no allocations. Of the values found in a text, the first
// from the left is masked, the longest of those that start there, then
// the first after it, and so on.
type redactor struct {
	// values is how many values there are.
	values int
	// longest is the length of the longest value.
	longest int
	// starts is the matcher of the values reversed: reading a text
	// backwards with it gives, for each place, the longest value that
	// starts there (findBackwards), which reading forwards can't in one
	// pass.
	starts *matcher
	// prefixes is the matcher of the values as they are, for a live
	// writer: what tail of the text so far may be a value's start.
	prefixes func() *matcher
}

// newRedactor masks the values of env, a release's resolved environment.
// Variables the platform injects (PORT, DOCK_*: users can't set them,
// validKey) aren't secret. A multi-line value (a PEM key) is masked line
// by line, as logs print it. It returns nil when nothing is to be masked.
func newRedactor(env map[string]string) *redactor {
	var secrets []string
	for k, v := range env {
		if validKey(k) != nil {
			continue
		}
		for line := range strings.Lines(v) {
			if line = strings.TrimRight(line, "\r\n"); len(line) >= minSecretLen {
				secrets = append(secrets, line)
			}
		}
	}
	if len(secrets) == 0 {
		return nil
	}
	slices.Sort(secrets)
	secrets = slices.Compact(secrets)
	r := &redactor{values: len(secrets)}
	// The values reversed, as pieces of one string.
	var all []byte
	for _, s := range secrets {
		r.longest = max(r.longest, len(s))
		all = append(all, s...)
		slices.Reverse(all[len(all)-len(s):])
	}
	reversed, rest := make([]string, len(secrets)), string(all)
	for i, s := range secrets {
		reversed[i], rest = rest[:len(s)], rest[len(s):]
	}
	r.starts = newMatcher(reversed)
	// Only `dock run` writes live, so most redactors never build this.
	r.prefixes = sync.OnceValue(func() *matcher { return newMatcher(secrets) })
	return r
}

// line masks a log line, reporting whether it had a secret. A line
// without one is returned as it is, without allocating.
func (r *redactor) line(s string) (string, bool) {
	if r == nil {
		return s, false
	}
	var few [4]found
	at := findBackwards(r.starts, s, few[:0])
	if len(at) == 0 {
		return s, false
	}
	out, _ := maskFound(make([]byte, 0, len(s)), s, len(s), at)
	return string(out), true
}

// appendMasked appends src to dst with its secrets masked, and returns it
// with how much of src it used. Unless final (src is all there is), it
// stops short of the last longest-1 bytes (hold), where a secret, or a
// longer one than what is there, may begin that ends in text not read
// yet. It never stops inside a secret: one that starts before hold ends
// within src, so it is found whole.
func (r *redactor) appendMasked(dst, src []byte, final bool) ([]byte, int) {
	hold := len(src)
	if !final {
		hold = max(0, len(src)-(r.longest-1))
	}
	var at []found
	return r.appendMaskedTo(dst, src, hold, &at)
}

// appendMaskedTo is appendMasked stopping at hold: secrets that start
// before it are masked whole, and src[hold:] (past the last of those) is
// left for later. at is the caller's scratch space, kept between calls.
func (r *redactor) appendMaskedTo(dst, src []byte, hold int, at *[]found) ([]byte, int) {
	*at = findBackwards(r.starts, src, (*at)[:0])
	return maskFound(dst, src, hold, *at)
}

// maskFound appends src to dst up to hold, with the values found in it
// (at, as findBackwards leaves them: last first) masked: each one that
// starts before hold and not inside the one masked before it. It returns
// how much of src it used: hold, or more when a value runs past it.
func maskFound[T string | []byte](dst []byte, src T, hold int, at []found) ([]byte, int) {
	pos := 0
	for _, f := range slices.Backward(at) {
		if f.at < pos {
			continue
		}
		if f.at >= hold {
			break
		}
		dst = append(append(dst, src[pos:f.at]...), secretMask...)
		pos = f.at + f.n
	}
	end := max(pos, hold)
	return append(dst, src[pos:end]...), end
}

// redactChunk is how much a redacting reader reads at a time.
const redactChunk = 32 << 10

// reader masks the secrets in what rc reads, streaming: between reads it
// holds back at most longest-1 bytes, where a secret may continue. A nil
// redactor returns rc itself.
func (r *redactor) reader(rc io.ReadCloser) io.ReadCloser {
	if r == nil {
		return rc
	}
	// Masking never grows text, so out fits in a buffer the size of in.
	size := redactChunk + r.longest
	rr := &redactReader{src: rc, red: r, in: make([]byte, 0, size), out: make([]byte, 0, size)}
	rr.at = rr.few[:0]
	return rr
}

// redactReader is redactor.reader's ReadCloser.
type redactReader struct {
	src io.ReadCloser
	red *redactor
	in  []byte // read from src, not masked yet
	out []byte // masked, out[off:] not returned yet
	off int
	err error     // src's error (io.EOF at the end), once it returned one
	at  []found   // appendMaskedTo's scratch space
	few [16]found // at's first space: a read with a few secrets allocates nothing
}

// Read implements io.Reader.
func (rr *redactReader) Read(p []byte) (int, error) {
	for rr.off == len(rr.out) {
		if rr.err != nil {
			return 0, rr.err
		}
		n, err := rr.src.Read(rr.in[len(rr.in):cap(rr.in)])
		rr.in = rr.in[:len(rr.in)+n]
		rr.err = err
		var used int
		hold := len(rr.in)
		if err == nil {
			hold = max(0, hold-(rr.red.longest-1))
		}
		rr.out, used = rr.red.appendMaskedTo(rr.out[:0], rr.in, hold, &rr.at)
		rr.off = 0
		rr.in = rr.in[:copy(rr.in, rr.in[used:])]
	}
	n := copy(p, rr.out[rr.off:])
	rr.off += n
	return n, nil
}

// Close implements io.Closer.
func (rr *redactReader) Close() error { return rr.src.Close() }

// writer masks the secrets in what is written to w, streaming, for a
// live terminal: unlike reader it holds back only a tail that could be
// the start of a secret, so a prompt shows at once. Flush writes what
// it held. A nil redactor returns w itself and a no-op flush.
func (r *redactor) writer(w io.Writer) (io.Writer, func() error) {
	if r == nil {
		return w, func() error { return nil }
	}
	rw := &redactWriter{dst: w, red: r}
	return rw, rw.flush
}

// redactWriter is redactor.writer's Writer. Writes come from one
// goroutine (node.JobStream), so it needs no lock.
type redactWriter struct {
	dst     io.Writer
	red     *redactor
	pending []byte // written, not masked yet: a tail a secret may start in
	out     []byte
	at      []found // appendMaskedTo's scratch space
}

// Write implements io.Writer: it masks what can't be part of a secret
// still being written, and keeps the rest for the next write.
func (rw *redactWriter) Write(p []byte) (int, error) {
	rw.pending = append(rw.pending, p...)
	hold := len(rw.pending) - rw.red.secretPrefixAt(rw.pending)
	var used int
	rw.out, used = rw.red.appendMaskedTo(rw.out[:0], rw.pending, hold, &rw.at)
	rw.pending = rw.pending[:copy(rw.pending, rw.pending[used:])]
	if len(rw.out) > 0 {
		if _, err := rw.dst.Write(rw.out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// flush writes what Write held back, masked.
func (rw *redactWriter) flush() error {
	if len(rw.pending) == 0 {
		return nil
	}
	out, _ := rw.red.appendMaskedTo(rw.out[:0], rw.pending, len(rw.pending), &rw.at)
	rw.pending = rw.pending[:0]
	_, err := rw.dst.Write(out)
	return err
}

// secretPrefixAt is the length of the longest tail of b that is the start
// (not all) of a secret: what a live writer must hold back. Such a tail
// is shorter than the longest secret, so only that much of b is read.
func (r *redactor) secretPrefixAt(b []byte) int {
	return r.prefixes().tail(b[max(0, len(b)-(r.longest-1)):])
}

// publishBuildLog publishes a build log line: once when it shows no
// secret, else as written to subscribers that may reveal secrets and
// masked to the rest. Masking happens once per line however many
// subscribers there are, and each copy is encoded once.
func (c *Core) publishBuildLog(ss svcScope, red *redactor, l apitypes.BuildLogLine) {
	masked, ok := red.line(l.Text)
	if !ok {
		c.publish(ss, apitypes.Event{Type: apitypes.EventBuildLog, BuildLog: &l})
		return
	}
	raw := l
	c.publishTo(ss, apitypes.Event{Type: apitypes.EventBuildLog, BuildLog: &raw}, secretsRevealed)
	l.Text = masked
	c.publishTo(ss, apitypes.Event{Type: apitypes.EventBuildLog, BuildLog: &l}, secretsMasked)
}
