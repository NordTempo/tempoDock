package core

import (
	"bytes"
	"cmp"
	"io"
	"slices"
	"strings"

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

// revealsSecrets reports whether a may see secret values: the rule
// ListVariables applies to reveal.
func (a Actor) revealsSecrets() bool {
	return a.require(apitypes.ScopeAdmin, "revealing secret values") == nil
}

// logRedactor returns what masks rel's secret values in its logs for a:
// nil when a may reveal them or there are none, so they get logs as
// written at no cost. It reads no more than the release row it is given.
func (c *Core) logRedactor(a Actor, rel store.Release) (*redactor, error) {
	if a.revealsSecrets() {
		return nil, nil
	}
	cfg, err := c.openConfig(rel.Config)
	if err != nil {
		return nil, err
	}
	return newRedactor(cfg.Env), nil
}

// redactor masks a fixed set of secret values in log text. A nil
// redactor masks nothing. Each value is searched for with the standard
// library's vectorized Index: a service has tens of variables at most, and
// a line without secrets costs one scan per value and no allocations.
type redactor struct {
	// secrets are the values, longest first so that of two starting at
	// the same place the longer is masked; raw is the same as bytes.
	secrets []string
	raw     [][]byte
	// longest is the length of the longest value.
	longest int
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
	slices.SortFunc(secrets, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	secrets = slices.Compact(secrets)
	r := &redactor{secrets: secrets, raw: make([][]byte, len(secrets)), longest: len(secrets[0])}
	for i, s := range secrets {
		r.raw[i] = []byte(s)
	}
	return r
}

// line masks a log line, reporting whether it had a secret. A line
// without one is returned as it is, without allocating.
func (r *redactor) line(s string) (string, bool) {
	if r == nil || !slices.ContainsFunc(r.secrets, func(v string) bool { return strings.Contains(s, v) }) {
		return s, false
	}
	out, _ := r.appendMasked(make([]byte, 0, len(s)), []byte(s), true)
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
	pos := 0
	for {
		at, n := r.next(src[pos:])
		if at < 0 || pos+at >= hold {
			break
		}
		dst = append(append(dst, src[pos:pos+at]...), secretMask...)
		pos += at + n
	}
	end := max(pos, hold)
	return append(dst, src[pos:end]...), end
}

// next finds the first secret in s: its index (-1 if none) and length.
// Once one is found, later (shorter) values are only looked for where
// they would start before it.
func (r *redactor) next(s []byte) (at, n int) {
	at = -1
	for _, v := range r.raw {
		window := s
		if at >= 0 {
			window = s[:min(len(s), at-1+len(v))]
		}
		if i := bytes.Index(window, v); i >= 0 && (at < 0 || i < at) {
			at, n = i, len(v)
		}
	}
	return at, n
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
	return &redactReader{src: rc, red: r, in: make([]byte, 0, size), out: make([]byte, 0, size)}
}

// redactReader is redactor.reader's ReadCloser.
type redactReader struct {
	src io.ReadCloser
	red *redactor
	in  []byte // read from src, not masked yet
	out []byte // masked, out[off:] not returned yet
	off int
	err error // src's error (io.EOF at the end), once it returned one
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
		rr.out, used = rr.red.appendMasked(rr.out[:0], rr.in, err != nil)
		rr.off = 0
		rr.in = rr.in[:copy(rr.in, rr.in[used:])]
	}
	n := copy(p, rr.out[rr.off:])
	rr.off += n
	return n, nil
}

// Close implements io.Closer.
func (rr *redactReader) Close() error { return rr.src.Close() }

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
