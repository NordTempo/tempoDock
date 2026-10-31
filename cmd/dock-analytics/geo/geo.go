// Package geo looks up where an address prefix is (ADR 0037): DB-IP's IP
// to City Lite database (CC BY 4.0: "IP Geolocation by DB-IP",
// https://db-ip.com), downloaded into the store's volume and replaced
// monthly. The ingest has already cut the address to its /24 (IPv6 /48);
// the store looks the prefix up and drops it.
package geo

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/NordTempo/tempoDock/cmd/dock-analytics/store"
)

// Attribution is what the UI shows wherever locations appear, as the
// licence asks.
const Attribution = "IP Geolocation by DB-IP"

// fileName is the database in the directory.
const fileName = "dbip-city-lite.mmdb"

// DownloadURL is where a month's database is, by year and month.
const DownloadURL = "https://download.db-ip.com/free/dbip-city-lite-%04d-%02d.mmdb.gz"

// maxDownload caps the download (the database is about 130 MB).
const maxDownload = 1 << 30

// DB is the current database; it is safe for concurrent use and may have
// none yet.
type DB struct {
	dir string
	// url is DownloadURL; tests change it.
	url string
	// updating serializes Update, so two downloads don't race.
	updating sync.Mutex

	mu     sync.RWMutex
	reader *maxminddb.Reader
	// month is the database's month, "2026-10"; "" for none.
	month string
}

// Open loads the database in dir, if there is one.
func Open(dir string) (*DB, error) {
	db := &DB{dir: dir, url: DownloadURL}
	path := filepath.Join(dir, fileName)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return db, nil
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		return db, fmt.Errorf("open %s: %w", path, err)
	}
	month, _ := os.ReadFile(filepath.Join(dir, "month"))
	db.reader, db.month = r, string(month)
	return db, nil
}

// Month is the database's month, "" for none.
func (db *DB) Month() string {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.month
}

// record is the part of a city record the store keeps.
type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

// Locate implements store.Locator: the prefix's country (ISO code),
// region and city, empty when unknown or without a database.
func (db *DB) Locate(prefix string) store.Location {
	a, err := netip.ParseAddr(prefix)
	if err != nil {
		return store.Location{}
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.reader == nil {
		return store.Location{}
	}
	var r record
	if err := db.reader.Lookup(a).Decode(&r); err != nil {
		return store.Location{}
	}
	loc := store.Location{Country: r.Country.ISOCode, City: r.City.Names["en"]}
	if len(r.Subdivisions) > 0 {
		loc.Region = r.Subdivisions[0].Names["en"]
	}
	return loc
}

// Update makes sure the database is the one of t's month: it downloads it
// (falling back to the month before, which DB-IP serves until the new one
// is out) unless it has it already, and swaps it in. It reports the month
// it has.
func (db *DB) Update(ctx context.Context, t time.Time) (string, error) {
	db.updating.Lock()
	defer db.updating.Unlock()
	want := t.UTC().Format("2006-01")
	if db.Month() == want {
		return want, nil
	}
	if err := os.MkdirAll(db.dir, 0o700); err != nil {
		return db.Month(), err
	}
	var errs []error
	for _, m := range []time.Time{t.UTC(), t.UTC().AddDate(0, -1, 0)} {
		month := m.Format("2006-01")
		if month == db.Month() {
			return month, nil // last month's is what there is
		}
		err := db.download(ctx, fmt.Sprintf(db.url, m.Year(), int(m.Month())), month)
		if err == nil {
			return month, nil
		}
		errs = append(errs, err)
	}
	return db.Month(), errors.Join(errs...)
}

// download fetches url into the directory as month's database and swaps
// it in once it opens.
func (db *DB) download(ctx context.Context, url, month string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, maxDownload))
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	tmp, err := os.CreateTemp(db.dir, ".download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, io.LimitReader(zr, maxDownload)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// The reader maps the file, so it stays valid across the rename; the
	// old one keeps its own file until it is closed after the swap.
	r, err := maxminddb.Open(tmp.Name())
	if err != nil {
		return fmt.Errorf("the download from %s isn't a database: %w", url, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(db.dir, fileName)); err != nil {
		_ = r.Close()
		return err
	}
	db.mu.Lock()
	old := db.reader
	db.reader, db.month = r, month
	db.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return os.WriteFile(filepath.Join(db.dir, "month"), []byte(month), 0o600)
}
