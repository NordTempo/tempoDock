package core

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

// benchEnv is a realistic service environment: 20 variables, a few of
// them short or injected (not masked).
func benchEnv() map[string]string {
	env := map[string]string{
		"PORT": "8080", "DOCK_SERVICE": "web", "DOCK_PROJECT": "shop", "NODE_ENV": "production", "LOG_LEVEL": "info",
		"DATABASE_URL": "postgres://shop:Zx8vQ2mN4pL7wR1t@postgres:5432/shop?sslmode=disable",
		"REDIS_URL":    "redis://:aS9dF3gH6jK2@redis:6379/0",
		"STRIPE_KEY":   "sk_live_51HxQ2bK8mN3pL9rT4vW7yZ0aC5dE6fG",
		"SESSION_KEY":  "c2VjcmV0LXNlc3Npb24ta2V5LWZvci1iZW5jaA",
	}
	for i := range 11 {
		env[fmt.Sprintf("FEATURE_%d", i)] = fmt.Sprintf("value-%d-%x", i, i*7919)
	}
	return env
}

// benchLogLines are 100 typical app log lines, one of which prints a
// secret.
func benchLogLines() []string {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf(`2026-11-03T10:%02d:%02d.123Z INFO http request method=GET path=/api/orders/%d status=200 duration=%dms ip=10.0.%d.7`, i/60, i%60, 1000+i, i%40, i%255)
	}
	lines[50] = "2026-11-03T10:00:50.123Z WARN connecting to postgres://shop:Zx8vQ2mN4pL7wR1t@postgres:5432/shop?sslmode=disable"
	return lines
}

// BenchmarkRedactLine is the cost per container log line for someone who
// can't reveal secrets: 100 lines, one with a secret, per 100 ops.
func BenchmarkRedactLine(b *testing.B) {
	red := newRedactor(benchEnv())
	lines := benchLogLines()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		_, _ = red.line(lines[i%len(lines)])
		i++
	}
}

// BenchmarkRedactBuildLog reads a 1 MB build log through the redacting
// reader, as BuildLog serves it to someone who can't reveal secrets.
func BenchmarkRedactBuildLog(b *testing.B) {
	red := newRedactor(benchEnv())
	var log bytes.Buffer
	for log.Len() < 1<<20 {
		for _, l := range benchLogLines() {
			log.WriteString(l)
			log.WriteByte('\n')
		}
	}
	data := log.Bytes()
	buf := make([]byte, 32<<10)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		r := red.reader(io.NopCloser(bytes.NewReader(data)))
		if _, err := io.CopyBuffer(io.Discard, struct{ io.Reader }{r}, buf); err != nil {
			b.Fatal(err)
		}
	}
}
