package docker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

// The probes' errors read back as what they mean (a deploy diagnosis
// depends on it, node.ParseHealthFailure).
func TestProbeErrorsParse(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	err := probeHTTP(context.Background(), srv.URL+"/health")
	if got := node.ParseHealthFailure(err.Error()); got.Kind != node.HealthFailStatus || got.Status != 404 {
		t.Errorf("404: %v parsed as %+v", err, got)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // nothing listens there now
	err = probeTCP(context.Background(), port)
	if got := node.ParseHealthFailure(err.Error()); got.Kind != node.HealthFailNotListening {
		t.Errorf("closed port: %v parsed as %+v", err, got)
	}
}
