package data

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func TestAppendCell(t *testing.T) {
	long := strings.Repeat("a", apitypes.DataCellMaxBytes-1) + "é" // the cut lands inside é
	cases := []struct {
		v    []byte
		kind CellKind
		full bool
		want string
	}{
		{nil, CellNumber, false, `null`},
		{[]byte("t"), CellBool, false, `true`},
		{[]byte("f"), CellBool, false, `false`},
		{[]byte("-12"), CellNumber, false, `-12`},
		{[]byte("1.5e-07"), CellNumber, false, `1.5e-07`},
		{[]byte("NaN"), CellNumber, false, `"NaN"`},
		{[]byte("-Infinity"), CellNumber, false, `"-Infinity"`},
		{[]byte("01"), CellNumber, false, `"01"`},
		{[]byte("12345678901234567890"), CellString, false, `"12345678901234567890"`},
		{[]byte("a\"b\\c\n\x01é\xff"), CellString, false, `"a\"b\\c\n\u0001é�"`},
		{[]byte("x"), CellMasked, false, `{"masked":true}`},
		{[]byte(long), CellString, false, `{"truncated":"` + strings.Repeat("a", apitypes.DataCellMaxBytes-1) + `","bytes":8193}`},
		{[]byte(long), CellString, true, `"` + long + `"`},
	}
	for _, c := range cases {
		if got := string(AppendCell(nil, c.v, c.kind, c.full)); got != c.want {
			t.Errorf("%q: got %.80s, want %.80s", c.v, got, c.want)
		}
	}
}
