package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A stream announces `ready` once it is subscribed, so an action taken
// after it is never missed; release events carry their sequence as `id:`.
func TestEventStreamReadyAndIDs(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/v1/events?project=shop", nil)
	req.Header.Set("Authorization", "Bearer "+f.tokens["deploy"])
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Body.Close() }()
	sc := bufio.NewScanner(stream.Body)
	if !sc.Scan() || sc.Text() != "event: ready" {
		t.Fatalf("first line %q, want the ready event", sc.Text())
	}

	if resp, body := f.do("deploy", "POST", shopProd+"/services/web/releases", apitypes.DeployImageRequest{}); resp.StatusCode != 202 {
		t.Fatalf("deploy: %d %s", resp.StatusCode, body)
	}
	var id string
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "id: "); ok {
			id = v
			continue
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok && id != "" {
			var ev apitypes.Event
			if err := json.Unmarshal([]byte(v), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.ReleaseEvent == nil || strconv.FormatInt(ev.ReleaseEvent.Seq, 10) != id {
				t.Fatalf("id %s for event %+v", id, ev)
			}
			// History has the same sequence numbers.
			_, body := f.do("read", "GET", shopProd+"/services/web/releases/1", nil)
			var rel apitypes.Release
			_ = json.Unmarshal(body, &rel)
			if len(rel.Events) == 0 || rel.Events[0].Seq != ev.ReleaseEvent.Seq {
				t.Fatalf("history %+v, live seq %d", rel.Events, ev.ReleaseEvent.Seq)
			}
			return
		}
	}
	t.Fatal("no release event with an id")
}

// A stream that fails before it is live answers with a plain problem.
func TestStreamErrorBeforeReadyIsProblem(t *testing.T) {
	f := newFixture(t)
	if resp, _ := f.do("deploy", "POST", shopProd+"/services/web/releases", apitypes.DeployImageRequest{}); resp.StatusCode != 202 {
		t.Fatal("deploy failed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.rt.Running()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Removed outside the platform.
	ctrs, _ := f.rt.List(context.Background())
	_ = f.rt.Stop(context.Background(), ctrs[0].ID, 0)
	if err := f.rt.Remove(context.Background(), ctrs[0].ID); err != nil {
		t.Fatal(err)
	}
	resp, body := f.do("read", "GET", shopProd+"/services/web/logs", nil)
	if resp.StatusCode != 404 || !strings.Contains(string(body), "is gone") {
		t.Fatalf("logs of a removed container: %d %s", resp.StatusCode, body)
	}
}
