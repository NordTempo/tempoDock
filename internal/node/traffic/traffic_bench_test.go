package traffic

import (
	"fmt"
	"testing"
)

// BenchmarkCounterAdd is the node's cost per proxied request on a counted
// route: parsing Caddy's line and counting it.
func BenchmarkCounterAdd(b *testing.B) {
	lines := make([][]byte, 64)
	for i := range lines {
		lines[i] = line("shop.example.com", "GET", fmt.Sprintf("/products/%d?ref=home", i), 200,
			fmt.Sprintf("203.0.113.%d", i), "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36")
	}
	c := NewCounter()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		c.Add(lines[i%len(lines)])
		i++
	}
}
