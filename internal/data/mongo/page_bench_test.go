package mongo

import (
	"fmt"
	"testing"

	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

// BenchmarkAppendDocumentRow encodes one page of documents (100 orders with a
// nested customer, an items array, a decimal and a date) into a reused
// buffer as Page does, with one sensitive path masked.
func BenchmarkAppendDocumentRow(b *testing.B) {
	docs := make([]wire.D, 100)
	for i := range docs {
		d, err := wire.ParseDoc(fmt.Sprintf(`{_id: ObjectId("6745f0c2e1b7a3d4c5f2a%03d"), status: "open", total: NumberDecimal("249.00"),
			customer: {email: "user%d@example.com", name: "Customer %d", tier: "gold"},
			items: [{sku: "TS-01", qty: 2}, {sku: "MUG-4", qty: 1}], createdAt: ISODate("2026-11-27T09:14:02Z")}`, i, i, i))
		if err != nil {
			b.Fatal(err)
		}
		docs[i] = d
	}
	cols := []string{"_id", "status", "total", "customer", "items", "createdAt"}
	masked := []string{"customer.email"}
	var buf, scratch []byte
	b.ReportAllocs()
	for b.Loop() {
		buf = buf[:0]
		for _, d := range docs {
			buf = appendRow(buf, d, cols, masked, false, &scratch)
		}
	}
	b.SetBytes(int64(len(buf)))
}
