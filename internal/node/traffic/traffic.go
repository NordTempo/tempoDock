// Package traffic counts the requests Caddy serves, for traffic statistics
// (ADR 0033).
//
// Caddy writes an access log line for each request to a counted route and
// sends it, one UDP datagram per line, to a loopback port the node listens
// on (Listen). The Counter parses each line and adds it to the hostname's
// counts: requests, status classes, bytes, time taken and the most
// requested paths. Lines are dropped once counted; nothing is written to
// disk. The control plane drains the counts
// once a minute (node.Traffic).
//
// UDP keeps the cost off the apps: Caddy never waits for the node, and a
// node that is down or behind loses lines instead of slowing requests.
package traffic

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	// maxHosts bounds the hostnames counted between two drains. Caddy only
	// logs routed hostnames, so it is only reached by datagrams that
	// didn't come from Caddy.
	maxHosts = 4096
	// maxPaths bounds the paths counted per hostname between two drains.
	// Past it the least requested path gives way to the new one
	// (Space-Saving), so a scanner probing thousands of paths can't push
	// out the real ones.
	maxPaths = 256
	// DrainPaths is how many of a hostname's paths a drain reports.
	DrainPaths = 50
	// maxLine is the largest datagram read; Caddy's lines are far smaller.
	maxLine = 64 << 10
)

// Counter adds up access log lines per hostname. It is safe for concurrent
// use.
type Counter struct {
	mu    sync.Mutex
	hosts map[string]*hostCount
	// addr is where Listen listens.
	addr net.Addr
}

// hostCount is one hostname's counts since the last drain.
type hostCount struct {
	node.HostTraffic
	paths map[pathKey]uint64
}

type pathKey struct{ method, path string }

var _ node.Traffic = (*Counter)(nil)

// NewCounter returns an empty Counter.
func NewCounter() *Counter {
	return &Counter{hosts: map[string]*hostCount{}}
}

// accessLine is the part of a Caddy access log line that is counted.
type accessLine struct {
	Request struct {
		Method string `json:"method"`
		Host   string `json:"host"`
		URI    string `json:"uri"`
	} `json:"request"`
	// Duration is in seconds, Caddy's default.
	Duration float64 `json:"duration"`
	Size     uint64  `json:"size"`
	Status   int     `json:"status"`
}

// Add counts one access log line. Lines that don't parse are ignored.
func (c *Counter) Add(line []byte) {
	var l accessLine
	if json.Unmarshal(line, &l) != nil || l.Request.Host == "" {
		return
	}
	host := l.Request.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	class := 0 // Caddy logs 0 when Go wrote the 200 itself
	switch {
	case l.Status >= 500:
		class = 3
	case l.Status >= 400:
		class = 2
	case l.Status >= 300:
		class = 1
	}
	key := pathKey{normalizeMethod(l.Request.Method), normalizePath(l.Request.URI)}

	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.hosts[host]
	if h == nil {
		if len(c.hosts) >= maxHosts {
			return
		}
		h = &hostCount{HostTraffic: node.HostTraffic{Host: strings.Clone(host)}, paths: map[pathKey]uint64{}}
		c.hosts[h.Host] = h
	}
	h.Requests++
	h.Status[class]++
	h.Bytes += l.Size
	h.Duration += time.Duration(l.Duration * float64(time.Second))
	h.countPath(key)
}

// countPath adds a request to key, making room with Space-Saving when the
// table is full: the least requested path is replaced, and the new one
// inherits its count plus one, an upper bound on its real count.
func (h *hostCount) countPath(key pathKey) {
	if n, ok := h.paths[key]; ok {
		h.paths[key] = n + 1
		return
	}
	var start uint64
	if len(h.paths) >= maxPaths {
		var least pathKey
		start = ^uint64(0)
		for k, n := range h.paths {
			if n < start {
				least, start = k, n
			}
		}
		delete(h.paths, least)
	}
	h.paths[pathKey{key.method, strings.Clone(key.path)}] = start + 1
}

// DrainTraffic implements node.Traffic.
func (c *Counter) DrainTraffic(context.Context) ([]node.HostTraffic, error) {
	c.mu.Lock()
	hosts := c.hosts
	c.hosts = make(map[string]*hostCount, len(hosts))
	c.mu.Unlock()

	out := make([]node.HostTraffic, 0, len(hosts))
	for _, h := range hosts {
		t := h.HostTraffic
		t.Paths = make([]node.PathCount, 0, len(h.paths))
		for k, n := range h.paths {
			t.Paths = append(t.Paths, node.PathCount{Method: k.method, Path: k.path, Requests: n})
		}
		slices.SortFunc(t.Paths, func(a, b node.PathCount) int {
			return cmp.Or(cmp.Compare(b.Requests, a.Requests), strings.Compare(a.Path, b.Path), strings.Compare(a.Method, b.Method))
		})
		t.Paths = t.Paths[:min(len(t.Paths), DrainPaths)]
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b node.HostTraffic) int { return strings.Compare(a.Host, b.Host) })
	return out, nil
}

// Listen counts the access log lines sent to addr, a loopback host:port,
// until ctx ends. The read blocks between datagrams: an idle server wakes
// nothing.
func Listen(ctx context.Context, addr string, log *slog.Logger) (*Counter, error) {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	// Room for bursts while the reader is busy (the kernel may cap it).
	_ = conn.(*net.UDPConn).SetReadBuffer(1 << 20)
	c := NewCounter()
	c.addr = conn.LocalAddr()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go func() {
		buf := make([]byte, maxLine)
		for {
			n, _, err := conn.ReadFrom(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				log.Debug("traffic: read", "err", err)
				continue
			}
			c.Add(buf[:n])
		}
	}()
	return c, nil
}
