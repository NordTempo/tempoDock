package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A source upload that goes silent is cut after uploadIdle and answered
// with what happened, so it doesn't hold one of the server's upload
// places for as long as its client keeps the connection open; the next
// upload to the service goes through.
func TestSilentUploadIsCutOff(t *testing.T) {
	old := uploadIdle
	uploadIdle = 200 * time.Millisecond
	t.Cleanup(func() { uploadIdle = old })
	f := newFixture(t)

	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	req, _ := http.NewRequest("POST", f.srv.URL+shopProd+"/services/web/releases/source", pr)
	req.Header.Set("Authorization", "Bearer "+f.tokens["deploy"])
	req.Header.Set("Content-Type", "application/x-tar")
	type answer struct {
		status int
		body   string
	}
	done := make(chan answer, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- answer{0, err.Error()}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		done <- answer{resp.StatusCode, string(b)}
	}()
	// One byte, then nothing.
	if _, err := pw.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.status != http.StatusBadRequest || !strings.Contains(got.body, "stopped sending") {
			t.Fatalf("the silent upload was answered %d: %s", got.status, got.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the silent upload is still held open")
	}
	if res, body := f.upload("deploy", shopProd+"/services/web/releases/source"); res.StatusCode != http.StatusAccepted {
		t.Fatalf("the next upload: %d: %s", res.StatusCode, body)
	}
}
