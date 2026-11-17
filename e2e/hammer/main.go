// Command hammer sends a steady stream of GET requests to one URL and reports
// failures, for checking zero-downtime deploys. It runs until -d elapses or it
// gets SIGINT/SIGTERM, then prints a summary: totals, and when each failure
// reason and each distinct response body was first and last seen (to see
// when traffic switched, and whether failures sit at the switch).
//
//	go run ./e2e/hammer -url http://web-p.example/ -c 8 -d 60s
//
// -connect ADDR sends every request to ADDR whatever the URL's host
// resolves to (curl's --connect-to), for servers without DNS like e2e/vm's.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type seen struct {
	first, last time.Time
	count       int
}

type stats struct {
	mu       sync.Mutex
	start    time.Time
	total    int
	failures map[string]*seen
	bodies   map[string]*seen
}

func (s *stats) record(body, failure string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total++
	m, key := s.bodies, body
	if failure != "" {
		m, key = s.failures, failure
	}
	now := time.Now()
	b := m[key]
	if b == nil {
		b = &seen{first: now}
		m[key] = b
	}
	b.last, b.count = now, b.count+1
}

func main() {
	url := flag.String("url", "", "URL to request")
	conc := flag.Int("c", 8, "concurrent workers")
	dur := flag.Duration("d", time.Minute, "how long to run")
	rate := flag.Duration("every", 10*time.Millisecond, "pause between requests per worker")
	connect := flag.String("connect", "", "host:port to dial instead of the URL's host")
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "hammer: -url is required")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *dur)
	defer cancel()

	client := &http.Client{Timeout: 5 * time.Second}
	if *connect != "" {
		var d net.Dialer
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, *connect)
		}
		client.Transport = tr
	}
	st := &stats{start: time.Now(), failures: map[string]*seen{}, bodies: map[string]*seen{}}
	var wg sync.WaitGroup
	for range *conc {
		wg.Go(func() {
			for ctx.Err() == nil {
				if body, failure := get(ctx, client, *url); ctx.Err() == nil {
					st.record(body, failure)
				}
				time.Sleep(*rate)
			}
		})
	}
	wg.Wait()

	failed := 0
	for _, f := range st.failures {
		failed += f.count
	}
	fmt.Printf("requests=%d failed=%d duration=%s\n", st.total, failed, time.Since(st.start).Round(time.Millisecond))
	for _, reason := range slices.Sorted(maps.Keys(st.failures)) {
		f := st.failures[reason]
		fmt.Printf("  failure %q x%d first=+%s last=+%s\n", reason, f.count,
			f.first.Sub(st.start).Round(time.Millisecond), f.last.Sub(st.start).Round(time.Millisecond))
	}
	for body, b := range st.bodies {
		fmt.Printf("  body %q x%d first=+%s last=+%s\n", body, b.count,
			b.first.Sub(st.start).Round(time.Millisecond), b.last.Sub(st.start).Round(time.Millisecond))
	}
}

// get returns the trimmed body, or a failure reason.
func get(ctx context.Context, c *http.Client, url string) (string, string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.Do(req)
	if err != nil {
		msg := err.Error()
		if i := strings.LastIndex(msg, ": "); i >= 0 {
			msg = msg[i+2:]
		}
		return "", "error: " + msg
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Sprintf("status %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(b)), ""
}
