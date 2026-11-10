package pg

import (
	"strconv"
	"testing"

	"github.com/NordTempo/tempoDock/internal/data"
)

// BenchmarkAppendRow encodes one rows frame (500 rows of 8 typical columns)
// into a reused buffer, as Query and Page do.
func BenchmarkAppendRow(b *testing.B) {
	kinds := []data.CellKind{data.CellNumber, data.CellString, data.CellString, data.CellBool, data.CellNumber, data.CellString, data.CellString, data.CellString}
	src := []int{0, 1, 2, 3, 4, 5, 6, 7}
	rows := make([][][]byte, 500)
	for i := range rows {
		rows[i] = [][]byte{
			[]byte(strconv.Itoa(i)),
			[]byte("customer name " + strconv.Itoa(i)),
			[]byte("2026-10-20 14:02:00+00"),
			[]byte("t"),
			[]byte("120.5"),
			[]byte("00000000-0000-0000-0000-000000000042"),
			[]byte(`{"status": "paid", "note": "line\nbreak"}`),
			nil,
		}
	}
	var buf []byte
	b.ReportAllocs()
	for b.Loop() {
		buf = buf[:0]
		for _, r := range rows {
			buf = appendRow(buf, r, src, kinds, false)
		}
	}
	b.SetBytes(int64(len(buf)))
}
