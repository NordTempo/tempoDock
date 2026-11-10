package node

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A deploy's health check (Containers.HealthCheck) fails with an error
// whose message says why. It crosses the node link as text, so core reads
// it back with ParseHealthFailure for a deploy diagnosis. The messages
// about the container itself are made here (ExitedError); the probes'
// ("connect to ...", "GET ... returned 404 Not Found") are the Docker
// runtime's, and its tests run them through the parser too.

// HealthFailureKind is what ParseHealthFailure found a failed health check
// to say.
type HealthFailureKind string

const (
	// HealthFailUnknown is a message the parser doesn't know.
	HealthFailUnknown HealthFailureKind = ""
	// HealthFailOOM: the kernel killed the container for its memory limit.
	HealthFailOOM HealthFailureKind = "oom"
	// HealthFailKilled: SIGKILL (exit code 137) with a memory limit, most
	// likely out of memory on a kernel that doesn't report OOM kills.
	HealthFailKilled HealthFailureKind = "killed"
	// HealthFailExited: the container exited with ExitCode.
	HealthFailExited HealthFailureKind = "exited"
	// HealthFailNoPort: a health check path, but no published port.
	HealthFailNoPort HealthFailureKind = "no_port"
	// HealthFailStatus: the HTTP check answered Status, outside 2xx/3xx.
	HealthFailStatus HealthFailureKind = "status"
	// HealthFailNotListening: nothing accepted the connection, or it was
	// closed at once (docker-proxy does that when nothing listens).
	HealthFailNotListening HealthFailureKind = "not_listening"
	// HealthFailNoAnswer: connected, but no answer before the timeout.
	HealthFailNoAnswer HealthFailureKind = "no_answer"
)

// HealthFailure is a failed health check read back from its message.
type HealthFailure struct {
	Kind HealthFailureKind
	// ExitCode is set for exited and killed.
	ExitCode int
	// LimitMB is the memory limit an oom or killed message names (0: none).
	LimitMB int
	// Status is the HTTP status of a status failure.
	Status int
}

// ExitedError is the health check's error for a container that died while
// it was being waited on. limit is its memory limit in bytes (0: none).
func ExitedError(code int, oom bool, limit int64) error {
	switch {
	case oom && limit > 0:
		return fmt.Errorf("container ran out of memory (limit %d MB) and was killed during health check", limit>>20)
	case oom:
		return errors.New("container ran out of memory and was killed during health check")
	case code == 137 && limit > 0:
		// SIGKILL. Some kernels don't report the OOM kill to Docker, so
		// name the likely cause without claiming it.
		return fmt.Errorf("container was killed (exit code 137) during health check, most likely out of memory (limit %d MB)", limit>>20)
	}
	return fmt.Errorf("container exited with code %d during health check", code)
}

var (
	limitMB    = regexp.MustCompile(`\(limit (\d+) MB\)`)
	exitedCode = regexp.MustCompile(`container exited with code (\d+)`)
	httpStatus = regexp.MustCompile(`returned (\d{3})\b`)
)

// ParseHealthFailure reads what a failed health check's message says.
// The most specific statement wins: a check that timed out after the
// container died says it died.
func ParseHealthFailure(msg string) HealthFailure {
	limit := 0
	if m := limitMB.FindStringSubmatch(msg); m != nil {
		limit, _ = strconv.Atoi(m[1])
	}
	switch {
	case strings.Contains(msg, "container ran out of memory"):
		return HealthFailure{Kind: HealthFailOOM, ExitCode: 137, LimitMB: limit}
	case strings.Contains(msg, "container was killed (exit code 137)"):
		return HealthFailure{Kind: HealthFailKilled, ExitCode: 137, LimitMB: limit}
	}
	if m := exitedCode.FindStringSubmatch(msg); m != nil {
		code, _ := strconv.Atoi(m[1])
		return HealthFailure{Kind: HealthFailExited, ExitCode: code}
	}
	if strings.Contains(msg, "publishes no port") {
		return HealthFailure{Kind: HealthFailNoPort}
	}
	if m := httpStatus.FindStringSubmatch(msg); m != nil {
		status, _ := strconv.Atoi(m[1])
		return HealthFailure{Kind: HealthFailStatus, Status: status}
	}
	for _, s := range []string{"is the app listening", "connection refused", "connection reset by peer", ": EOF", "no route to host"} {
		if strings.Contains(msg, s) {
			return HealthFailure{Kind: HealthFailNotListening}
		}
	}
	for _, s := range []string{"Client.Timeout", "deadline exceeded", "i/o timeout", "health check timed out"} {
		if strings.Contains(msg, s) {
			return HealthFailure{Kind: HealthFailNoAnswer}
		}
	}
	return HealthFailure{}
}
