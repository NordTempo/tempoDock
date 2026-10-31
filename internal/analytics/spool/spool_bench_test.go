package spool

import "testing"

// BenchmarkAppend is the ingest's write of one event to the spool.
func BenchmarkAppend(b *testing.B) {
	s, err := Open(b.TempDir(), Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	line := []byte(`{"t":1798000000000,"s":12,"sv":40,"rl":981,"h":"shop.example.com","k":1,"p":"/products/kayak-pro","r":"news.ycombinator.com","ch":"social","v":13464654573299568789,"ip":"203.0.113.0","b":"Chrome","bv":"130","o":"macOS","ov":"15","d":"desktop","sw":1440,"sh":900,"l":"nb-NO"}`)
	b.ReportAllocs()
	for b.Loop() {
		if err := s.Append(line); err != nil {
			b.Fatal(err)
		}
	}
}
