package node

import "testing"

// The node's own messages, wrapped as a deploy reports them, read back.
func TestParseHealthFailure(t *testing.T) {
	const pre = "health check failed (GET /health on :8080, timeout 1m0s): "
	for _, tc := range []struct {
		msg  string
		want HealthFailure
	}{
		{pre + ExitedError(1, false, 512<<20).Error(), HealthFailure{Kind: HealthFailExited, ExitCode: 1}},
		{pre + ExitedError(127, false, 0).Error(), HealthFailure{Kind: HealthFailExited, ExitCode: 127}},
		{pre + ExitedError(137, true, 512<<20).Error(), HealthFailure{Kind: HealthFailOOM, ExitCode: 137, LimitMB: 512}},
		{pre + ExitedError(137, true, 0).Error(), HealthFailure{Kind: HealthFailOOM, ExitCode: 137}},
		{pre + ExitedError(137, false, 256<<20).Error(), HealthFailure{Kind: HealthFailKilled, ExitCode: 137, LimitMB: 256}},
		{pre + "health check path is set but the container publishes no port", HealthFailure{Kind: HealthFailNoPort}},
		{pre + "health check timed out after 1m0s: GET http://127.0.0.1:32768/health returned 404 Not Found", HealthFailure{Kind: HealthFailStatus, Status: 404}},
		{pre + "health check timed out after 1m0s: connect to 127.0.0.1:32768: connection closed, is the app listening on the container port?", HealthFailure{Kind: HealthFailNotListening}},
		{pre + "health check timed out after 1m0s: GET http://127.0.0.1:32768/health: EOF", HealthFailure{Kind: HealthFailNotListening}},
		{pre + "health check timed out after 1m0s: GET http://127.0.0.1:32768/health: context deadline exceeded (Client.Timeout exceeded while awaiting headers)", HealthFailure{Kind: HealthFailNoAnswer}},
		// dock-probe's own output (cmd/dock-probe), for a running service.
		{"GET /health returned 503", HealthFailure{Kind: HealthFailStatus, Status: 503}},
		{"connect to port 8080: connection refused, nothing listens on it", HealthFailure{Kind: HealthFailNotListening}},
		{"connect to port 8080: connection reset", HealthFailure{Kind: HealthFailNotListening}},
		{"GET /health: timed out", HealthFailure{Kind: HealthFailNoAnswer}},
		{"GET /health: connection closed without an answer", HealthFailure{Kind: HealthFailNoAnswer}},
		{"something else entirely", HealthFailure{}},
	} {
		if got := ParseHealthFailure(tc.msg); got != tc.want {
			t.Errorf("ParseHealthFailure(%q) = %+v, want %+v", tc.msg, got, tc.want)
		}
	}
}
