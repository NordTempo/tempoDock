package core

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestCaught: a panic in a worker's work comes back as an error with no
// internals in it and is logged once with its stack; a normal return
// passes through untouched.
func TestCaught(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))

	n, err := caught(log, "deploying a push", func() (int, error) {
		shift := -1
		return 1 << shift, nil
	})
	if err == nil || n != 0 || err.Error() != "deploying a push failed on a bug in Dock" {
		t.Fatalf("a panic: %d, %v", n, err)
	}
	if out := logged.String(); strings.Count(out, "panic in a background worker") != 1 || !strings.Contains(out, "negative shift amount") || !strings.Contains(out, "caught_test.go") {
		t.Fatalf("the log should hold the panic and its stack once: %s", out)
	}

	sentinel := errors.New("refused")
	if n, err := caught(log, "x", func() (int, error) { return 7, sentinel }); n != 7 || !errors.Is(err, sentinel) {
		t.Fatalf("a normal return: %d, %v", n, err)
	}
}
