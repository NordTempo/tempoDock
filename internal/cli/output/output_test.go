package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExitCodeOf(t *testing.T) {
	nf := &Error{Exit: ExitNotFound, Message: "no such service"}
	for err, want := range map[error]ExitCode{
		nil:                                   ExitOK,
		errors.New("boom"):                    ExitError,
		nf:                                    ExitNotFound,
		fmt.Errorf("deploy web: %w", nf):      ExitNotFound,
		fmt.Errorf("x: %w", MissingFlag("a")): ExitUsage,
	} {
		if got := ExitCodeOf(err); got != want {
			t.Errorf("ExitCodeOf(%v) = %d, want %d", err, got, want)
		}
	}
}

func TestPrintError(t *testing.T) {
	err := fmt.Errorf("deploy web: %w", &Error{Exit: ExitDeployFailed, Code: "health_check_failed", Message: "health check failed", Hint: "See `dock logs`."})

	var human bytes.Buffer
	(&IO{Err: &human}).PrintError(err)
	if got, want := human.String(), "error: deploy web: health check failed\nSee `dock logs`.\n"; got != want {
		t.Errorf("human error = %q, want %q", got, want)
	}

	var js bytes.Buffer
	(&IO{Err: &js, JSON: true}).PrintError(err)
	var out struct {
		Error struct {
			Code     string
			ExitCode int
			Message  string
			Hint     string
		}
	}
	if err := json.Unmarshal(js.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %q", js.String())
	}
	if out.Error.Code != "health_check_failed" || out.Error.ExitCode != 6 || out.Error.Hint == "" {
		t.Errorf("JSON error = %+v", out.Error)
	}
}

func TestPrintAndTable(t *testing.T) {
	var buf bytes.Buffer
	o := &IO{Out: &buf}
	err := o.Print([]string{"ignored"}, func() error {
		tb := o.Table("name", "server")
		tb.Row("prod", "https://a")
		tb.Row("homelab", "http://b")
		return tb.Flush()
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "NAME     SERVER\nprod     https://a\nhomelab  http://b\n"
	if buf.String() != want {
		t.Errorf("table = %q, want %q", buf.String(), want)
	}

	buf.Reset()
	o.JSON = true
	if err := o.Print(map[string]int{"n": 1}, func() error { t.Fatal("human output in JSON mode"); return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "{\n  \"n\": 1\n}" {
		t.Errorf("JSON = %q", buf.String())
	}
}

func TestPromptNonInteractiveNamesFlag(t *testing.T) {
	o := &IO{In: strings.NewReader("typed\n")}
	_, err := o.Prompt("Server URL", "server")
	if ExitCodeOf(err) != ExitUsage || !strings.Contains(err.Error(), "--server") {
		t.Errorf("got %v", err)
	}
}
