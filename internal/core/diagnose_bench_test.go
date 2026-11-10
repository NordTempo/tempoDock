package core

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// BenchmarkDiagnoseRules is every rule over a failed health check with a
// full container tail and 30 variables: the work a failure adds after
// its reads (ADR 0070).
func BenchmarkDiagnoseRules(b *testing.B) {
	env := map[string]string{"PORT": "8080"}
	for i := range 30 {
		env[fmt.Sprintf("VAR_%d", i)] = fmt.Sprintf("value-%d", i)
	}
	lines := make([]string, diagContainerLines)
	for i := range lines {
		lines[i] = fmt.Sprintf(`2026-11-10T14:01:%02dZ {"level":30,"time":1731247291000,"msg":"request completed","req":{"id":%d,"url":"/api/items"}}`, i%60, i)
	}
	lines[len(lines)-1] = `2026-11-10T14:01:59Z {"level":30,"msg":"Server listening at http://0.0.0.0:3000"}`
	msg := "health check timed out after 1m0s: connect to 127.0.0.1:20001: connection closed, is the app listening on the container port?"
	in := diagInput{service: "web", environment: "production", step: apitypes.ReleaseHealthChecking, code: apitypes.CodeHealthCheckFailed,
		err: msg, health: node.ParseHealthFailure(msg), container: lines,
		cfg: releaseConfig{Port: 8080, Published: true, HealthCheckPath: "/health", HealthCheckTimeout: 60, MemoryMB: 512, Env: env}}
	b.ReportAllocs()
	for b.Loop() {
		if r := diagnose(&in); r.cause.Rule != "port_mismatch" {
			b.Fatal(r.cause)
		}
	}
}
