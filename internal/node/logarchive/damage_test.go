package logarchive

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Archives written within one clock tick are ordered by release ID, as a
// number, so the service limit and the server-wide cap drop the older
// release, not the newer one.
func TestLimitsOnEqualMtimes(t *testing.T) {
	same := time.Now()
	setup := func(t *testing.T, archives [][2]string) (string, int64) {
		root := t.TempDir()
		var size int64
		for _, a := range archives {
			size = write(t, root, a[0], a[1], node.LogLimits{}, lines(0, 50))
			if err := os.Chtimes(filepath.Join(root, a[0], a[1]+ext), same, same); err != nil {
				t.Fatal(err)
			}
		}
		return root, size
	}
	for _, pair := range [][2]string{{"8", "9"}, {"9", "10"}} {
		root, size := setup(t, [][2]string{{"1", pair[0]}, {"1", pair[1]}})
		if _, err := Prune(root, node.LogPrune{KeepServices: []string{"1"}, Limits: node.LogLimits{MaxBytes: size + size/2}}); err != nil {
			t.Fatal(err)
		}
		if got := archives(t, root, "1"); !slices.Equal(got, []string{pair[1]}) {
			t.Fatalf("releases %v: the service limit kept %v", pair, got)
		}
	}
	root, size := setup(t, [][2]string{{"2", "9"}, {"1", "10"}})
	if _, err := Prune(root, node.LogPrune{KeepServices: []string{"1", "2"}, Limits: node.LogLimits{MaxTotalBytes: size + size/2}}); err != nil {
		t.Fatal(err)
	}
	if a, b := archives(t, root, "1"), archives(t, root, "2"); !slices.Equal(a, []string{"10"}) || len(b) != 0 {
		t.Fatalf("the server cap kept %v and %v", a, b)
	}
}

// noisy makes n lines of about 2 KB that gzip can't shrink much, so a
// member spans many writes and a cut lands mid-line.
func noisy(first, n int) []node.LogLine {
	rng := rand.New(rand.NewPCG(uint64(first), 1))
	out := make([]node.LogLine, n)
	for i := range out {
		b := make([]byte, 2048)
		for j := range b {
			b[j] = 'a' + byte(rng.IntN(26))
		}
		out[i] = node.LogLine{Time: t0.Add(time.Duration(first+i) * time.Second), Stream: "stdout", Text: fmt.Sprintf("%d %s", first+i, b)}
	}
	return out
}

// Two services archived at once stay within the per-service and the
// per-server caps, each keeps its newest archive, and every archive left
// reads whole.
func TestConcurrentWritesHoldCaps(t *testing.T) {
	root := t.TempDir()
	size := write(t, t.TempDir(), "1", "1", node.LogLimits{}, noisy(0, 20))
	limits := node.LogLimits{MaxBytes: 3*size + size/2, MaxTotalBytes: 5*size + size/2}
	var wg sync.WaitGroup
	for _, svc := range []string{"1", "2"} {
		wg.Go(func() {
			for rel := 1; rel <= 8; rel++ {
				_, err := Write(root, svc, strconv.Itoa(rel), limits, func(add func(node.LogLine) error) error {
					for _, l := range noisy(0, 20) {
						if err := add(l); err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	var total int64
	for _, svc := range []string{"1", "2"} {
		entries, err := os.ReadDir(filepath.Join(root, svc))
		if err != nil {
			t.Fatal(err)
		}
		var svcTotal int64
		for _, e := range entries {
			rel, ok := archiveRelease(e.Name())
			if !ok {
				t.Fatalf("left behind: %s/%s", svc, e.Name())
			}
			info, err := e.Info()
			if err != nil {
				t.Fatal(err)
			}
			svcTotal += info.Size()
			if got := read(t, root, node.LogArchiveQuery{Service: svc, Release: strconv.FormatUint(rel, 10)}); len(got) != 20 {
				t.Fatalf("%s/%d read %d lines", svc, rel, len(got))
			}
		}
		if svcTotal > limits.MaxBytes {
			t.Fatalf("service %s keeps %d bytes, cap %d", svc, svcTotal, limits.MaxBytes)
		}
		if got := archives(t, root, svc); !slices.Contains(got, "8") {
			t.Fatalf("service %s lost its newest archive: %v", svc, got)
		}
		total += svcTotal
	}
	if total > limits.MaxTotalBytes {
		t.Fatalf("server keeps %d bytes, cap %d", total, limits.MaxTotalBytes)
	}
}

// A read while members are appended returns whole lines only: a prefix
// of what was written, never half a line.
func TestReadDuringAppend(t *testing.T) {
	root := t.TempDir()
	const members, per = 12, 10
	var want []string
	for m := range members {
		for _, l := range noisy(m*per, per) {
			want = append(want, l.Text)
		}
	}
	write(t, root, "1", "1", node.LogLimits{}, noisy(0, per))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for m := 1; m < members; m++ {
			write(t, root, "1", "1", node.LogLimits{}, noisy(m*per, per))
		}
	}()
	q := node.LogArchiveQuery{Service: "1", Release: "1"}
	for reading := true; reading; {
		select {
		case <-done:
			reading = false
		default:
		}
		got := read(t, root, q)
		if len(got) < per || !slices.Equal(got, want[:min(len(got), len(want))]) {
			t.Fatalf("read %d lines, not a prefix of what was written", len(got))
		}
	}
	if got := read(t, root, q); !slices.Equal(got, want) {
		t.Fatalf("after the appends: %d lines, want %d", len(got), len(want))
	}
}

// A damaged archive reads up to the damage, without an error, whole and
// with a long tail: garbage after the last member, a corrupt first
// header, an empty file. (A member cut short is TestReadDamagedEnd.)
func TestReadDamage(t *testing.T) {
	all := []string{"line 0", "line 1", "line 2", "line 3"}
	for _, tc := range []struct {
		name   string
		damage func(b []byte) []byte
		want   int
	}{
		{"garbage appended", func(b []byte) []byte { return append(b, "not gzip at all"...) }, 4},
		{"bad first header", func(b []byte) []byte { b[0] = 0; return b }, 0},
		{"empty", func([]byte) []byte { return nil }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "1", "2", node.LogLimits{}, lines(0, 2))
			write(t, root, "1", "2", node.LogLimits{}, lines(2, 2))
			p := filepath.Join(root, "1", "2"+ext)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, tc.damage(b), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := read(t, root, node.LogArchiveQuery{Service: "1", Release: "2"}); !slices.Equal(got, all[:tc.want]) {
				t.Fatalf("read %v", got)
			}
			if got := read(t, root, node.LogArchiveQuery{Service: "1", Release: "2", Tail: 1000}); !slices.Equal(got, all[:tc.want]) {
				t.Fatalf("tail 1000 read %v", got)
			}
		})
	}
}
