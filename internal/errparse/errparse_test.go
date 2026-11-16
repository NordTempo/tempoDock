package errparse

import (
	"strings"
	"testing"
)

// Lines are judged by the level they state, as the logs pane judges
// them; error words count only without one, and stderr plays no part.
func TestLineLevel(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Level
	}{
		{`{"level":"error","msg":"db down"}`, LevelError},
		{`{"level":"info","ts":1730000000.1,"logger":"http.log.access","msg":"handled request","status":502}`, LevelNone},
		{`{"level":"info","msg":"retrying after error"}`, LevelNone},
		{`{"lvl": "WARN", "msg": "slow"}`, LevelWarn},
		{`{"severity":"CRITICAL","message":"x"}`, LevelError},
		{`{"level":50,"msg":"pino error"}`, LevelError},
		{`{"level":30,"msg":"pino info with error in it"}`, LevelNone},
		{`{"level":40,"msg":"pino warn"}`, LevelWarn},
		{`time=2026-11-07T12:00:00Z level=error msg="query failed"`, LevelError},
		{`level=info msg="no error here"`, LevelNone},
		{"2026-11-07T12:00:00.000Z\tERROR\thttp.log.error\tdial tcp: connection refused\t{\"status\":502}", LevelError},
		{"2026-11-07T12:00:00.000Z\tINFO\thttp.log.access\thandled request with error\t{}", LevelNone},
		{`{"level":"verbose","msg":"an exception"}`, LevelError}, // unknown level: the words decide
		{`Error: connect ECONNREFUSED 127.0.0.1:5432`, LevelError},
		{`panic: runtime error: index out of range`, LevelError},
		{`TypeError: x is undefined`, LevelNone}, // a stack trace makes it one (TestAssembler)
		{`GET /api/checkout 502 31ms`, LevelNone},
		{`no errors found, 0 failures`, LevelNone},
		{`listening on :8080`, LevelNone},
		// Redis states its level: "*" notice, "#" warning (its errors too).
		{`1:M 13 Nov 2026 11:25:50.521 * <bf> { bf-error-rate : 0.01 }`, LevelNone},
		{`1:C 13 Nov 2026 11:25:50.519 # WARNING Memory overcommit must be enabled!`, LevelWarn},
		{`1:M 13 Nov 2026 11:25:50.530 # Fatal error loading the DB, check server logs. Exiting.`, LevelError},
	} {
		if got := LineLevel(tc.line); got != tc.want {
			t.Errorf("LineLevel(%q) = %d, want %d", tc.line, got, tc.want)
		}
	}
}

// events feeds lines to an assembler and returns every event, flushed
// at the end.
func events(lines ...string) []Event {
	var a Assembler
	var out []Event
	for _, l := range lines {
		if ev, ok := a.Line(l); ok {
			out = append(out, ev)
		}
	}
	if ev, ok := a.Flush(); ok {
		out = append(out, ev)
	}
	return out
}

// one feeds lines and requires exactly one event.
func one(t *testing.T, lines ...string) Event {
	t.Helper()
	evs := events(lines...)
	if len(evs) != 1 {
		t.Fatalf("events = %+v, want one", evs)
	}
	return evs[0]
}

// A Go panic and its goroutine trace are one error, named by the panic,
// whose culprit is the first frame that isn't the runtime's.
func TestGoPanic(t *testing.T) {
	trace := []string{
		"panic: runtime error: invalid memory address or nil pointer dereference",
		"[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x4a1b2c]",
		"",
		"goroutine 1 [running]:",
		"panic({0x4b8f40?, 0xc000012345?})",
		"\t/usr/local/go/src/runtime/panic.go:770 +0x132",
		"main.(*Cart).Total(0x0)",
		"\t/app/cart.go:42 +0x1d",
		"main.main()",
		"\t/app/main.go:12 +0x25",
		"exit status 2",
	}
	ev := one(t, append(trace, "listening on :8080")...)
	if ev.Title != "panic: runtime error: invalid memory address or nil pointer dereference" || ev.Culprit != "main.(*Cart).Total (/app/cart.go:42)" {
		t.Fatalf("event = %+v", ev)
	}
	if !strings.HasPrefix(ev.Sample, trace[0]) || !strings.HasSuffix(ev.Sample, "exit status 2") {
		t.Fatalf("sample = %q", ev.Sample)
	}

	// The same panic after a deploy that moved the code: same issue.
	moved := strings.NewReplacer("cart.go:42 +0x1d", "cart.go:57 +0x3a", "0xc000012345", "0xc000099999").Replace(strings.Join(trace, "\n"))
	if again := one(t, strings.Split(moved, "\n")...); again.Fingerprint != ev.Fingerprint {
		t.Fatalf("fingerprint moved: %s, then %s", ev.Fingerprint, again.Fingerprint)
	}
	// Another function panicking is another issue.
	other := strings.ReplaceAll(strings.Join(trace, "\n"), "(*Cart).Total", "(*Cart).Add")
	if o := one(t, strings.Split(other, "\n")...); o.Fingerprint == ev.Fingerprint {
		t.Fatal("a panic in another function shares the fingerprint")
	}
}

// A recovered panic logged by net/http starts as a line and goes on as
// a goroutine trace.
func TestGoRecoveredPanic(t *testing.T) {
	ev := one(t,
		"2026/11/07 12:00:00 http: panic serving 10.0.0.1:41234: boom",
		"goroutine 34 [running]:",
		"net/http.(*conn).serve.func1()",
		"\t/usr/local/go/src/net/http/server.go:1898 +0xbe",
		"main.handler({0x7f, 0xc0}, 0xc000)",
		"\t/app/main.go:20 +0x1d",
	)
	if ev.Title != "http: panic serving 10.0.0.1:41234: boom" || ev.Culprit != "net/http.(*conn).serve.func1 (/usr/local/go/src/net/http/server.go:1898)" {
		t.Fatalf("event = %+v", ev)
	}
}

// A Python traceback is one error named by its last line; the culprit is
// the innermost frame.
func TestPythonTraceback(t *testing.T) {
	ev := one(t,
		"Traceback (most recent call last):",
		`  File "/app/app.py", line 30, in handle`,
		"    total = cart_total(items)",
		`  File "/app/cart.py", line 12, in cart_total`,
		"    return int(raw)",
		"ValueError: invalid literal for int() with base 10: 'abc'",
		"INFO: next request",
	)
	if ev.Title != "ValueError: invalid literal for int() with base 10: 'abc'" || ev.Culprit != "cart_total (/app/cart.py:12)" {
		t.Fatalf("event = %+v", ev)
	}
	// Another value in the message, same issue.
	again := one(t,
		"Traceback (most recent call last):",
		`  File "/app/app.py", line 31, in handle`,
		`  File "/app/cart.py", line 14, in cart_total`,
		"ValueError: invalid literal for int() with base 10: 'xyz'",
	)
	if again.Fingerprint != ev.Fingerprint {
		t.Fatalf("fingerprints %s and %s differ", ev.Fingerprint, again.Fingerprint)
	}
}

// A chained Python exception is one error, named by the last one raised.
func TestPythonChained(t *testing.T) {
	ev := one(t,
		"Traceback (most recent call last):",
		`  File "/app/a.py", line 1, in <module>`,
		"KeyError: 'id'",
		"",
		"During handling of the above exception, another exception occurred:",
		"",
		"Traceback (most recent call last):",
		`  File "/app/b.py", line 9, in load`,
		"RuntimeError: no user",
	)
	if ev.Title != "RuntimeError: no user" || ev.Culprit != "load (/app/b.py:9)" {
		t.Fatalf("event = %+v", ev)
	}
}

// A Node.js exception's "at" frames follow its first line, which alone
// isn't an error line; the culprit is the top frame.
func TestJSStack(t *testing.T) {
	ev := one(t,
		"GET /cart 200",
		"TypeError: Cannot read properties of undefined (reading 'price')",
		"    at Cart.total (/app/src/cart.js:42:13)",
		"    at async handler (/app/src/server.js:10:5)",
		"GET /health 200",
	)
	if ev.Title != "TypeError: Cannot read properties of undefined (reading 'price')" || ev.Culprit != "Cart.total (/app/src/cart.js:42:13)" {
		t.Fatalf("event = %+v", ev)
	}
	if !strings.HasPrefix(ev.Sample, "TypeError") || strings.Contains(ev.Sample, "GET") {
		t.Fatalf("sample = %q", ev.Sample)
	}
}

// Node prints an uncaught exception under the place it was thrown, the
// source line and a caret: that header is no error of its own, even
// when the line it quotes says Error, and the exception is one.
func TestNodeUncaught(t *testing.T) {
	for name, tc := range map[string]struct {
		lines []string
		title string
	}{
		"in a module": {[]string{
			"/app/node_modules/pg/lib/client.js:132",
			"      Error.captureStackTrace(err)",
			"            ^",
			"",
			"Error: connect ECONNREFUSED 127.0.0.1:5432",
			"    at TCPConnectWrap.afterConnect [as oncomplete] (node:net:1555:16) {",
			"  errno: -111,",
			"  code: 'ECONNREFUSED'",
			"}",
			"",
			"Node.js v20.11.0",
		}, "Error: connect ECONNREFUSED 127.0.0.1:5432"},
		"at the top level": {[]string{
			"/app/index.js:3",
			`throw new Error("boom");`,
			"^",
			"",
			"Error: boom",
			"    at Object.<anonymous> (/app/index.js:3:7)",
		}, "Error: boom"},
		"an unhandled error event": {[]string{
			"node:events:497",
			"      throw er; // Unhandled 'error' event",
			"      ^",
			"",
			"Error: listen EADDRINUSE: address already in use :::3000",
			"    at Server.setupListenHandle [as _listen2] (node:net:1872:16)",
			"Emitted 'error' event on Server instance at:",
			"    at emitErrorNT (node:net:1899:8) {",
			"  code: 'EADDRINUSE'",
			"}",
		}, "Error: listen EADDRINUSE: address already in use :::3000"},
	} {
		ev := one(t, tc.lines...)
		if ev.Title != tc.title {
			t.Errorf("%s: event = %+v", name, ev)
		}
	}
	// Without the caret it is an error line after a path, as written.
	if ev := one(t, "src/app.ts:12", "error: config missing", "listening on :8080"); ev.Sample != "error: config missing" {
		t.Errorf("an error line after a path: %+v", ev)
	}
}

// A Java logger's error line, the exception it logged and its frames are
// one error named by the exception; causes don't change the culprit.
func TestJavaStack(t *testing.T) {
	ev := one(t,
		"2026-11-07 12:00:00.123 ERROR 1 --- [main] c.e.App : Request failed",
		"java.lang.IllegalStateException: cart 4411 is closed",
		"\tat com.example.Cart.add(Cart.java:42)",
		"\tat com.example.Api.post(Api.java:17)",
		"Caused by: java.io.IOException: gone",
		"\tat com.example.Db.read(Db.java:5)",
		"\t... 12 more",
	)
	if ev.Title != "java.lang.IllegalStateException: cart 4411 is closed" || ev.Culprit != "com.example.Cart.add(Cart.java:42)" {
		t.Fatalf("event = %+v", ev)
	}
}

// Structured error lines are named by their message, and a stack they
// carry gives the culprit.
func TestStructuredLines(t *testing.T) {
	ev := one(t, `{"level":50,"time":1730980800000,"msg":"checkout failed","err":{"type":"Error","message":"card declined","stack":"Error: card declined\n    at charge (/app/pay.js:8:11)"}}`)
	if ev.Title != "checkout failed: Error: card declined" || ev.Culprit != "charge (/app/pay.js:8:11)" {
		t.Fatalf("pino event = %+v", ev)
	}
	ev = one(t, `time=2026-11-07T12:00:00Z level=error msg="query failed" err="dial tcp 10.0.0.5:5432: i/o timeout"`)
	if ev.Title != "query failed: dial tcp 10.0.0.5:5432: i/o timeout" {
		t.Fatalf("logfmt event = %+v", ev)
	}
	ev = one(t, "2026-11-07T12:00:00.000Z\tERROR\tapi/handler.go:31\tupstream refused\t{\"request_id\":\"8f14e45f\"}")
	if ev.Title != "upstream refused" {
		t.Fatalf("console event = %+v", ev)
	}
	ev = one(t, "2026-11-07 12:00:01 ERROR [worker] job 991 failed")
	if ev.Title != "[worker] job 991 failed" {
		t.Fatalf("plain event = %+v", ev)
	}
}

// Info lines, Caddy's access log included, are never errors, and a plain
// line costs nothing to remember.
func TestNotErrors(t *testing.T) {
	if evs := events(
		`{"level":"info","ts":1730980800.5,"logger":"http.log.access.log0","msg":"handled request","request":{"method":"GET","uri":"/"},"status":200}`,
		`{"level":"info","ts":1730980801.5,"logger":"http.log.access.log0","msg":"handled request","status":503}`,
		"listening on :8080",
		"    at the end of the day", // indented "at" without an exception before it
	); len(evs) != 0 {
		t.Fatalf("events = %+v", evs)
	}
	// Postgres as Dock starts, stops or restarts it.
	if evs := events(
		"2026-11-16 12:00:00.123 UTC [30] FATAL:  the database system is starting up",
		"2026-11-16 12:00:00.123 UTC [31] FATAL:  the database system is not yet accepting connections",
		"2026-11-16 12:00:00.123 UTC [32] FATAL:  the database system is shutting down",
		"2026-11-16 12:00:00.123 UTC [33] FATAL:  terminating connection due to administrator command",
	); len(evs) != 0 {
		t.Fatalf("Postgres's routine lines: %+v", evs)
	}
	if ev := one(t, `2026-11-16 12:00:00.123 UTC [34] FATAL:  password authentication failed for user "app"`); !strings.Contains(ev.Title, "password authentication failed") {
		t.Fatalf("a real FATAL: %+v", ev)
	}
}

// Numbers, IDs, quoted values and addresses don't split an issue.
func TestFingerprintIgnoresValues(t *testing.T) {
	a := one(t, `{"level":"error","msg":"user 41 order 550e8400-e29b-41d4-a716-446655440000 failed from 10.1.2.3:5555 with \"card\" id 0x7f3a9c trace 9f86d081884c7d65"}`)
	b := one(t, `{"level":"error","msg":"user 7 order 123e4567-e89b-12d3-a456-426614174000 failed from 192.168.0.9:80 with \"bank\" id 0xdeadbeef trace 2c26b46b68ffc68f"}`)
	c := one(t, `{"level":"error","msg":"user 7 order 123e4567-e89b-12d3-a456-426614174000 refused from 192.168.0.9:80"}`)
	if a.Fingerprint != b.Fingerprint {
		t.Fatalf("same error, fingerprints %s and %s", a.Fingerprint, b.Fingerprint)
	}
	if a.Fingerprint == c.Fingerprint {
		t.Fatal("another message shares the fingerprint")
	}
	if got := Normalize(`user 41 said 'hi' at 10.0.0.1:80, can't wait`); got != "user 0 said <str> at 0.0.0.0:0, can't wait" {
		t.Fatalf("Normalize = %q", got)
	}
}

// A trace longer than the caps is still one error, its sample cut.
func TestEventCaps(t *testing.T) {
	lines := []string{"Error: deep"}
	for range 500 {
		lines = append(lines, "    at recurse (/app/r.js:1:1) "+strings.Repeat("x", 100))
	}
	ev := one(t, lines...)
	if n := strings.Count(ev.Sample, "\n") + 1; n > MaxLines || len(ev.Sample) > MaxSample {
		t.Fatalf("sample has %d lines, %d bytes", n, len(ev.Sample))
	}
}

// npm's block is one error, named by the line that says what failed; a
// script stopped with SIGTERM (a stop or redeploy) is none.
func TestNpm(t *testing.T) {
	stopped := []string{
		"npm error path /app",
		"npm error command failed",
		"npm error signal SIGTERM",
		"npm error command sh -c node --require ./tracing.js server.js",
		"npm error A complete log of this run can be found in: /root/.npm/_logs/2026-11-13T06_58_19_792Z-debug-0.log",
	}
	if evs := events(append(stopped, "listening on :8080")...); len(evs) != 0 {
		t.Fatalf("a stopped script: %+v", evs)
	}
	// Exiting 1 is the app's failure, one issue whatever the log's name.
	failed := strings.Join(stopped, "\n")
	ev := one(t, strings.Split(strings.Replace(failed, "signal SIGTERM", "code 1", 1), "\n")...)
	if ev.Title != "npm error code 1" || !strings.HasSuffix(ev.Sample, "debug-0.log") {
		t.Fatalf("a failed script: %+v", ev)
	}
	again := strings.ReplaceAll(failed, "2026-11-13T06_58_19_792Z", "2026-11-14T08_01_02_003Z")
	if o := one(t, strings.Split(strings.Replace(again, "signal SIGTERM", "code 1", 1), "\n")...); o.Fingerprint != ev.Fingerprint {
		t.Fatal("the same failure, another issue")
	}
	// npm's own errors stay, in npm 10's words or older npm's.
	for _, lines := range [][]string{
		{"npm error Missing script: \"start\"", "npm error", "npm error To see a list of scripts, run:", "npm error   npm run"},
		{"npm ERR! code ENOENT", "npm ERR! syscall open", "npm ERR! path /app/package.json", "npm ERR! errno -2"},
	} {
		if ev := one(t, lines...); ev.Title != lines[0] {
			t.Errorf("npm's error: %+v", ev)
		}
	}
}
