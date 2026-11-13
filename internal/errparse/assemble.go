package errparse

import (
	"strings"
)

// Limits on one event: lines past them still belong to it (a long trace
// isn't split into many errors) but aren't kept in its sample.
const (
	// MaxLines and MaxSample cap an event's sample.
	MaxLines  = 50
	MaxSample = 8 << 10
	// maxTitle caps a title, in runes.
	maxTitle = 200
	// maxFrames is how many top frames a fingerprint takes.
	maxFrames = 3
)

// Event is one error: one line, or a stack trace's lines.
type Event struct {
	// Fingerprint is 16 hex digits, the same for the same error: its
	// type, its message without numbers, IDs and quoted values, and its
	// top three stack frames without line numbers.
	Fingerprint string
	// Title is the error's type and message, on one line; Culprit its
	// top frame as written, with its line ("main.handler
	// (/app/main.go:42)"), or "" without a stack.
	Title, Culprit string
	// Sample is the event's lines as written, at most MaxLines and
	// MaxSample bytes.
	Sample string
}

// mode is what an open event is.
type mode uint8

const (
	modeNone   mode = iota
	modeLine        // an error line, which a stack may follow
	modeStack       // a JavaScript or Java exception's "at" frames
	modeGo          // a Go panic and its goroutine traces
	modePython      // a Python traceback, up to its exception line
	modeNpm         // npm's "npm error" block, after its script ended
)

// Assembler turns one container's lines into events. Feed it every line
// in order with Line; Flush when the stream ends, or when an open event
// has had no line for a while. It keeps at most one event's sample
// (MaxSample) and the last line outside an event, so its memory is
// bounded. Not safe for concurrent use.
type Assembler struct {
	mode mode
	// head is the line naming the event's type and message.
	head string
	// lines counts the event's lines; sample holds those kept.
	lines  int
	sample []byte
	// frames are the event's top frames, normalized for the
	// fingerprint; culprit is the first as written.
	frames  []string
	culprit string
	// goFunc is a Go trace's function line waiting for its file line;
	// goroutines counts the trace's goroutines (only the first one's
	// frames count).
	goFunc     string
	goroutines int
	// cause: a Java exception's "Caused by" was read; its frames don't
	// count.
	cause bool
	// pyDone: a traceback's exception line was read; more follows only
	// for a chained exception. pyFrames are its frames, innermost last.
	pyDone   bool
	pyFrames []pyFrame
	// stopped: an npm block says its script was stopped (npmStopped).
	stopped bool
	// prev is the last line outside an event: a JavaScript or Java
	// exception's first line, if frames follow it.
	prev string
}

// pyFrame is a traceback's frame: normalized, and as written.
type pyFrame struct{ norm, raw string }

// Open reports whether an event is being assembled.
func (a *Assembler) Open() bool { return a.mode != modeNone }

// Line feeds the next line. It returns the event this line ended, if
// any: an event ends at the first line that doesn't continue it.
func (a *Assembler) Line(s string) (Event, bool) {
	if a.mode == modeNone {
		a.start(s)
		return Event{}, false
	}
	if a.continues(s) {
		a.add(s)
		return Event{}, false
	}
	ev, ok := a.finish()
	a.start(s)
	return ev, ok
}

// Flush ends the open event, if any, and returns it.
func (a *Assembler) Flush() (Event, bool) {
	if a.mode == modeNone {
		return Event{}, false
	}
	return a.finish()
}

// start opens an event if s begins one, and otherwise remembers it as
// the line before the next.
func (a *Assembler) start(s string) {
	switch {
	case strings.HasPrefix(s, "Traceback (most recent call last):"):
		a.mode = modePython
	case strings.HasPrefix(s, "panic: ") || strings.HasPrefix(s, "fatal error: "):
		a.mode, a.head = modeGo, s
	case isNpm(s):
		a.mode, a.head = modeNpm, s
		a.npmLine(s)
	case a.prev != "" && isAtFrame(s) && looksLikeException(a.prev):
		a.mode, a.head = modeStack, a.prev
		a.add(a.prev)
		a.continues(s)
	case LineLevel(s) == LevelError:
		a.mode, a.head = modeLine, s
	default:
		a.prev = s
		return
	}
	a.prev = ""
	a.add(s)
}

// add keeps s in the sample while there is room.
func (a *Assembler) add(s string) {
	a.lines++
	if a.lines > MaxLines || len(a.sample) >= MaxSample {
		return
	}
	if len(a.sample) > 0 {
		a.sample = append(a.sample, '\n')
	}
	room := MaxSample - len(a.sample)
	if len(s) > room {
		s = cutUTF8(s, room)
	}
	a.sample = append(a.sample, s...)
}

// continues reports whether s belongs to the open event, noting the
// frames and exception lines it brings.
func (a *Assembler) continues(s string) bool {
	switch a.mode {
	case modePython:
		return a.pythonLine(s)
	case modeGo:
		return a.goLine(s)
	case modeNpm:
		if !isNpm(s) {
			return false
		}
		a.npmLine(s)
		return true
	}
	switch {
	case strings.HasPrefix(s, "goroutine ") && strings.Contains(s, " ["):
		// A recovered panic's trace ("http: panic serving ...").
		a.mode = modeGo
		return a.goLine(s)
	case isAtFrame(s):
		a.mode = modeStack
		if !a.cause { // only the first exception's frames
			a.frame(strings.TrimPrefix(strings.TrimSpace(s), "at "))
		}
		return true
	case strings.HasPrefix(s, "Caused by: "):
		a.cause = true
		return true
	case s != "" && isSpace(s[0]):
		return true
	case a.mode == modeLine && a.lines == 1 && looksLikeException(s):
		// A logger's error line, then the exception it logged (Java).
		a.head = s
		return true
	}
	return false
}

// pythonLine continues a traceback.
func (a *Assembler) pythonLine(s string) bool {
	switch {
	case strings.HasPrefix(s, "Traceback (most recent call last):"):
		a.pyDone, a.pyFrames = false, a.pyFrames[:0]
		return true
	case strings.HasPrefix(s, "During handling of the above exception") || strings.HasPrefix(s, "The above exception was the direct cause"):
		return true
	case s == "":
		return true
	case a.pyDone:
		return false
	case isSpace(s[0]):
		if f, ok := pythonFrame(s); ok {
			a.pyFrames = append(a.pyFrames, f)
			if len(a.pyFrames) > maxFrames {
				a.pyFrames = append(a.pyFrames[:0], a.pyFrames[len(a.pyFrames)-maxFrames:]...)
			}
		}
		return true
	}
	// The exception: "ValueError: invalid literal".
	a.head, a.pyDone = s, true
	return true
}

// pythonFrame reads `  File "/app/x.py", line 12, in handler`.
func pythonFrame(s string) (pyFrame, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), `File "`)
	if !ok {
		return pyFrame{}, false
	}
	file, rest, ok := strings.Cut(rest, `"`)
	if !ok {
		return pyFrame{}, false
	}
	line := ""
	if r, ok := strings.CutPrefix(rest, ", line "); ok {
		line = r[:digits(r)]
	}
	fn := ""
	if _, f, ok := strings.Cut(rest, ", in "); ok {
		fn = f
	}
	raw := fn + " (" + file + ":" + line + ")"
	return pyFrame{norm: Normalize(fn + " (" + file + ")"), raw: raw}, true
}

// npmRest is the text of one of npm's error lines after its prefix
// ("npm error " from npm 10 on, "npm ERR! " before), which a blank line
// of the block lacks; ok is false for another line.
func npmRest(s string) (rest string, ok bool) {
	if len(s) < 8 || s[0] != 'n' {
		return "", false
	}
	for _, p := range [...]string{"npm error", "npm ERR!"} {
		if r, ok := strings.CutPrefix(s, p); ok && (r == "" || r[0] == ' ') {
			return strings.TrimPrefix(r, " "), true
		}
	}
	return "", false
}

// isNpm reports whether s is one of npm's error lines.
func isNpm(s string) bool {
	_, ok := npmRest(s)
	return ok
}

// npmLine notes a line of npm's block: a block is one error, named by its
// first line that says more than where and what ran ("npm error code
// ENOENT", "npm error signal SIGTERM", "npm error Missing script:
// start"), and dropped when its script was stopped (npmStopped).
func (a *Assembler) npmLine(s string) {
	rest, _ := npmRest(s)
	if npmStopped(rest) {
		a.stopped = true
	}
	head, _ := npmRest(a.head)
	if npmBoilerplate(head) && !npmBoilerplate(rest) {
		a.head = s
	}
}

// npmStopped reports whether an npm line says its script ended on
// SIGTERM or SIGINT (or exited 143 or 130, as a script that handles them
// does): something asked it to stop, Dock's own stop or redeploy above
// all, which is no error of the app's. A crash ends on another signal
// (SIGKILL when out of memory, SIGSEGV) or exit code, and stays one.
func npmStopped(rest string) bool {
	switch rest {
	case "signal SIGTERM", "signal SIGINT", "code 143", "code 130":
		return true
	}
	return false
}

// npmBoilerplate reports whether an npm line only says where and what
// ran, or where its debug log went.
func npmBoilerplate(rest string) bool {
	if rest == "" {
		return true
	}
	for _, p := range [...]string{"path ", "command", "errno ", "syscall ", "A complete log of this run"} {
		if strings.HasPrefix(rest, p) {
			return true
		}
	}
	return false
}

// goLine continues a Go trace.
func (a *Assembler) goLine(s string) bool {
	switch {
	case s == "":
		return true
	case strings.HasPrefix(s, "goroutine ") && strings.Contains(s, " ["):
		a.goroutines++
		a.goFunc = ""
		return true
	case s[0] == '\t':
		if a.goFunc != "" && a.goroutines == 1 {
			loc, _, _ := strings.Cut(strings.TrimSpace(s), " ")
			a.goFrame(a.goFunc, loc)
		}
		a.goFunc = ""
		return true
	case strings.HasPrefix(s, "[signal ") || strings.HasPrefix(s, "exit status ") ||
		strings.HasPrefix(s, "created by ") || strings.HasPrefix(s, "...additional frames elided..."):
		a.goFunc = ""
		return true
	case strings.HasSuffix(s, ")") && strings.IndexByte(s, '(') > 0 && !strings.ContainsAny(s[:1], " \t"):
		a.goFunc = s
		return true
	}
	return false
}

// goFrame notes a Go frame: its function without arguments, and where.
// The runtime's own frames (the panic itself) don't count.
func (a *Assembler) goFrame(fn, loc string) {
	if i := strings.LastIndexByte(fn, '('); i > 0 {
		fn = fn[:i]
	}
	if fn == "panic" || strings.HasPrefix(fn, "runtime.") {
		return
	}
	file := loc
	if i := strings.LastIndexByte(loc, ':'); i > 0 {
		file = loc[:i]
	}
	if len(a.frames) == 0 {
		a.culprit = fn + " (" + loc + ")"
	}
	if len(a.frames) < maxFrames {
		a.frames = append(a.frames, Normalize(fn+" ("+file+")"))
	}
}

// frame notes a JavaScript or Java frame, as written after "at ".
// Normalizing turns its line and column into zeros.
func (a *Assembler) frame(f string) {
	if len(a.frames) == 0 {
		a.culprit = f
	}
	if len(a.frames) < maxFrames {
		a.frames = append(a.frames, Normalize(f))
	}
}

// finish builds the open event and resets for the next; ok is false for
// one that is no error (a stopped npm script).
func (a *Assembler) finish() (ev Event, ok bool) {
	if a.stopped {
		a.reset()
		return Event{}, false
	}
	typ, msg, stack := "", "", ""
	switch a.mode {
	case modeGo:
		if rest, ok := strings.CutPrefix(a.head, "panic: "); ok {
			typ, msg = "panic", strings.TrimSuffix(rest, " [recovered]")
		} else if rest, ok := strings.CutPrefix(a.head, "fatal error: "); ok {
			typ, msg = "fatal error", rest
		} else {
			msg, stack = lineMessage(a.head)
		}
	case modePython:
		typ, msg = splitException(a.head)
		if typ == "" && msg == "" {
			typ = "Traceback"
		}
		for i := len(a.pyFrames) - 1; i >= 0; i-- {
			if len(a.frames) == 0 {
				a.culprit = a.pyFrames[i].raw
			}
			a.frames = append(a.frames, a.pyFrames[i].norm)
		}
	default:
		if looksLikeException(a.head) {
			typ, msg = splitException(a.head)
		} else {
			msg, stack = lineMessage(a.head)
			if t, m := splitException(msg); t != "" && looksLikeException(msg) {
				typ, msg = t, m
			}
		}
	}
	if len(a.frames) == 0 && stack != "" {
		a.stackFrames(stack)
	}
	title := typ
	switch {
	case typ == "":
		title = msg
	case msg != "":
		title = typ + ": " + msg
	}
	ev = Event{
		Fingerprint: Fingerprint(typ, msg, a.frames),
		Title:       oneLine(title, maxTitle),
		Culprit:     oneLine(a.culprit, maxTitle),
		Sample:      string(a.sample),
	}
	if stack != "" && len(ev.Sample)+len(stack)+1 <= MaxSample {
		ev.Sample += "\n" + stack
	}
	a.reset()
	return ev, true
}

// reset forgets the event just ended.
func (a *Assembler) reset() {
	a.mode, a.head, a.lines = modeNone, "", 0
	a.frames, a.culprit, a.goFunc, a.goroutines, a.cause = a.frames[:0], "", "", 0, false
	a.pyDone, a.pyFrames, a.stopped = false, a.pyFrames[:0], false
	// An idle follower keeps no sample buffer; errors are rare enough to
	// allocate one each.
	a.sample = nil
}

// stackFrames reads frames from a stack a structured log line carried
// in a field: JavaScript's or Java's "at" lines, Python's File lines or
// Go's function and file lines.
func (a *Assembler) stackFrames(stack string) {
	var py []pyFrame
	fn := ""
	for line := range strings.Lines(stack) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case isAtFrame(line):
			a.frame(strings.TrimPrefix(strings.TrimSpace(line), "at "))
		case strings.HasPrefix(strings.TrimSpace(line), `File "`):
			if f, ok := pythonFrame(line); ok {
				py = append(py, f)
			}
		case line != "" && line[0] == '\t' && fn != "":
			loc, _, _ := strings.Cut(strings.TrimSpace(line), " ")
			a.goFrame(fn, loc)
			fn = ""
		case line != "" && !isSpace(line[0]):
			fn = line
		}
	}
	for i := len(py) - 1; i >= 0 && len(a.frames) < maxFrames; i-- {
		if len(a.frames) == 0 {
			a.culprit = py[i].raw
		}
		a.frames = append(a.frames, py[i].norm)
	}
}

// isAtFrame reports whether s is an indented "at ..." stack frame.
func isAtFrame(s string) bool {
	if s == "" || !isSpace(s[0]) {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(s, " \t"), "at ")
}

// looksLikeException reports whether s starts with an exception's type:
// a name ending in Error or Exception ("TypeError: x",
// "java.lang.IllegalStateException", "Uncaught RangeError: y").
func looksLikeException(s string) bool {
	s = strings.TrimPrefix(s, "Uncaught ")
	n := 0
	for n < len(s) && (isWord(s[n]) || s[n] == '.' || s[n] == '$') {
		n++
	}
	if n == len(s) || s[n] == ':' || s[n] == ' ' {
		name := s[:n]
		return strings.HasSuffix(name, "Error") || strings.HasSuffix(name, "Exception")
	}
	return false
}

// splitException splits "TypeError: message" into its type and message.
func splitException(s string) (typ, msg string) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "Uncaught ")
	if t, m, ok := strings.Cut(s, ": "); ok && !strings.ContainsAny(t, " \t") {
		return t, strings.TrimSpace(m)
	}
	if !strings.ContainsAny(s, " \t") {
		return s, ""
	}
	return "", s
}

// oneLine collapses s's whitespace and cuts it to n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// cutUTF8 cuts s to at most n bytes on a rune boundary.
func cutUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
