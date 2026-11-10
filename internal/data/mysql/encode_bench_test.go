package mysql

import (
	"strconv"
	"testing"

	"github.com/NordTempo/tempoDock/internal/data"
)

// BenchmarkAppendRow encodes one rows frame (500 rows of 8 typical columns,
// one of them binary) into a reused buffer, as Query and Page do.
func BenchmarkAppendRow(b *testing.B) {
	cols := []outCol{
		{0, data.CellString, false}, {1, data.CellString, false}, {2, data.CellString, false}, {3, data.CellNumber, false},
		{4, data.CellString, false}, {5, data.CellString, true}, {6, data.CellString, false}, {7, data.CellString, false},
	}
	rows := make([][][]byte, 500)
	for i := range rows {
		rows[i] = [][]byte{
			[]byte(strconv.Itoa(i)),
			[]byte("customer name " + strconv.Itoa(i)),
			[]byte("2026-10-20 14:02:00"),
			[]byte("1"),
			[]byte("120.50"),
			{0x00, 0xff, 0x10, 0x20},
			[]byte(`{"status": "paid", "note": "line\nbreak"}`),
			nil,
		}
	}
	var buf, scratch []byte
	b.ReportAllocs()
	for b.Loop() {
		buf = buf[:0]
		for _, r := range rows {
			buf = appendRow(buf, r, cols, false, &scratch)
		}
	}
	b.SetBytes(int64(len(buf)))
}
