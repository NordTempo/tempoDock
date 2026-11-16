package gateway

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// BenchmarkGatewayRead is the gateway's hot path: a signed GET of a small
// object in the spool (a link file, the most frequent read of a push or
// pull), through the HTTP stack, verification and the file serve.
func BenchmarkGatewayRead(b *testing.B) {
	dir := b.TempDir()
	g, err := Open(Options{Dir: dir, MaxBytes: 1 << 30, AccessKey: "registry", Secret: "s3cret", Timing: Timing{Waiting: time.Hour}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(g.Close)
	srv := newBenchServer(b, g)
	c := &s3.Client{Bucket: s3.Bucket{Endpoint: srv, Name: "dock", PathStyle: true, AccessKeyID: "registry", SecretAccessKey: "s3cret"}}
	ctx := context.Background()
	const link = "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if err := c.Put(ctx, uploadKey, strings.NewReader(link), int64(len(link))); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		resp, err := c.Open(ctx, uploadKey, "")
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// newBenchServer serves g on loopback for the length of b.
func newBenchServer(b *testing.B, g *Gateway) string {
	srv := httptest.NewServer(g)
	b.Cleanup(srv.Close)
	return srv.URL
}
