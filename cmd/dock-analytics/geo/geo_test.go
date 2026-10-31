package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestWithoutADatabase(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if loc := db.Locate("203.0.113.0"); loc.Country != "" || db.Month() != "" {
		t.Errorf("no database located %+v (month %q)", loc, db.Month())
	}
}

func TestUpdateKeepsWhatItHasOnAFailedDownload(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked++
		_, _ = w.Write([]byte("not gzip"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	db, _ := Open(dir)
	db.url = srv.URL + "/%04d-%02d"
	at := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	if month, err := db.Update(context.Background(), at); err == nil || month != "" || asked != 2 {
		t.Errorf("a bad download: month %q, err %v, %d asks (this month's, then last month's)", month, err, asked)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a failed download left %v", entries)
	}
	// Already this month's: nothing is downloaded.
	db.month = "2026-10"
	if month, err := db.Update(context.Background(), at); err != nil || month != "2026-10" || asked != 2 {
		t.Errorf("current: %q, %v, %d asks", month, err, asked)
	}
}
