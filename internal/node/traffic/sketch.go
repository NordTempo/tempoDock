package traffic

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
)

const (
	sketchBits = 10
	// sketchSize is the number of registers: a standard error of about 3%.
	sketchSize = 1 << sketchBits
)

// Sketch is a HyperLogLog sketch of distinct visitors: 1 KiB however many
// it has seen, and two sketches merge into the sketch of both. Nodes build
// one per hostname and minute; the control plane merges them per service
// and hour, and across a range for its visitor count (ADR 0033). Only the
// registers are kept, never the hashes, so a sketch can't name a visitor.
type Sketch [sketchSize]uint8

// Add counts the visitor whose key hashes to h (Hash).
func (s *Sketch) Add(h uint64) {
	i := h >> (64 - sketchBits)
	// The low bit set keeps the rank within 64-sketchBits+1.
	rank := uint8(bits.LeadingZeros64(h<<sketchBits|1<<(sketchBits-1))) + 1
	if rank > s[i] {
		s[i] = rank
	}
}

// Merge adds o's visitors to s.
func (s *Sketch) Merge(o *Sketch) {
	for i, r := range o {
		if r > s[i] {
			s[i] = r
		}
	}
}

// Empty reports whether s has seen no visitor.
func (s *Sketch) Empty() bool { return *s == Sketch{} }

// Estimate is the number of distinct visitors s has seen. Small counts use
// linear counting, which is close to exact.
func (s *Sketch) Estimate() uint64 {
	sum, zeros := 0.0, 0
	for _, r := range s {
		sum += math.Ldexp(1, -int(r))
		if r == 0 {
			zeros++
		}
	}
	const m = float64(sketchSize)
	e := 0.7213 / (1 + 1.079/m) * m * m / sum
	if e <= 2.5*m && zeros > 0 {
		e = m * math.Log(m/float64(zeros))
	}
	return uint64(math.Round(e))
}

// Encoding: empty for an empty sketch; 0 and every register for a full
// one; 1 and (index uint16, rank) triples for a sparse one, which is what
// most sites' minutes and hours are.
const (
	encDense  = 0
	encSparse = 1
)

// MarshalBinary encodes s in its smaller form.
func (s *Sketch) MarshalBinary() ([]byte, error) {
	n := 0
	for _, r := range s {
		if r != 0 {
			n++
		}
	}
	switch {
	case n == 0:
		return nil, nil
	case 3*n >= sketchSize:
		return append([]byte{encDense}, s[:]...), nil
	}
	out := make([]byte, 1, 1+3*n)
	out[0] = encSparse
	for i, r := range s {
		if r != 0 {
			out = binary.BigEndian.AppendUint16(out, uint16(i))
			out = append(out, r)
		}
	}
	return out, nil
}

var errSketch = errors.New("traffic: invalid visitor sketch")

// UnmarshalBinary decodes what MarshalBinary encoded, replacing s.
func (s *Sketch) UnmarshalBinary(b []byte) error {
	*s = Sketch{}
	if len(b) == 0 {
		return nil
	}
	switch {
	case b[0] == encDense && len(b) == 1+sketchSize:
		copy(s[:], b[1:])
		return nil
	case b[0] == encSparse && (len(b)-1)%3 == 0:
		for p := b[1:]; len(p) > 0; p = p[3:] {
			i := binary.BigEndian.Uint16(p)
			if i >= sketchSize {
				*s = Sketch{}
				return errSketch
			}
			s[i] = p[2]
		}
		return nil
	}
	return errSketch
}

// Hash hashes a visitor (client address and user agent) for Add: FNV-1a
// with a final mix, so the top bits are well spread. It is fixed, not
// seeded per process, so sketches from before a restart and from another
// server merge with today's.
func Hash(addr, userAgent string) uint64 {
	const prime = 1099511628211
	h := uint64(14695981039346656037)
	for i := 0; i < len(addr); i++ {
		h = (h ^ uint64(addr[i])) * prime
	}
	h = (h ^ 0xff) * prime // a byte no address has, between the two
	for i := 0; i < len(userAgent); i++ {
		h = (h ^ uint64(userAgent[i])) * prime
	}
	// MurmurHash3's finalizer.
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}
