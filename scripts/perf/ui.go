package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Assets index.html loads up front: the entry script, the chunks Vite
// modulepreloads, stylesheets, and preloaded fonts; plus the fonts those
// stylesheets declare (fontURLRe).
var (
	entryRe       = regexp.MustCompile(`<script[^>]*type="module"[^>]*src="([^"]+)"`)
	preloadRe     = regexp.MustCompile(`<link[^>]*rel="modulepreload"[^>]*href="([^"]+)"`)
	stylesheetRe  = regexp.MustCompile(`<link[^>]*rel="stylesheet"[^>]*href="([^"]+)"`)
	fontPreloadRe = regexp.MustCompile(`<link[^>]*rel="preload"[^>]*href="([^"]+\.(?:woff2?|ttf|otf))"`)
	fontURLRe     = regexp.MustCompile(`url\(\s*["']?([^"')?#]+\.(?:woff2?|ttf|otf))`)
)

// checkUI measures what a first page load downloads: initial JS and CSS
// gzipped (as a browser receives them compressed), and fonts as shipped.
func checkUI(o options, t *table) error {
	a, err := initialAssets(o.dist)
	if err != nil {
		return err
	}
	t.within("UI initial JS (gzip)", fmt.Sprintf("%s (%d files)", kb(a.js), a.jsFiles), "≤ "+kb(initialJSBudget), a.js <= initialJSBudget)
	t.within("UI initial CSS (gzip)", kb(a.css), "≤ "+kb(cssBudget), a.css <= cssBudget)
	t.within("UI initial fonts", fmt.Sprintf("%s (%d files)", kb(a.fonts), a.fontFiles), "≤ "+kb(initialFontsBudget), a.fonts <= initialFontsBudget)

	var fonts, allJS int64
	var fontFiles, jsFiles int
	err = filepath.WalkDir(o.dist, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		switch filepath.Ext(strings.TrimSuffix(p, ".gz")) {
		case ".woff2", ".woff", ".ttf", ".otf":
			info, err := e.Info()
			if err != nil {
				return err
			}
			fonts += info.Size()
			fontFiles++
		case ".js":
			n, err := gzipSize(strings.TrimSuffix(p, ".gz"))
			if err != nil {
				return err
			}
			allJS += n
			jsFiles++
		}
		return nil
	})
	if err != nil {
		return err
	}
	t.within("UI fonts", fmt.Sprintf("%s (%d files)", kb(fonts), fontFiles), "≤ "+kb(fontsBudget), fonts <= fontsBudget)
	t.add("UI all JS incl. lazy routes (gzip)", fmt.Sprintf("%s (%d files)", kb(allJS), jsFiles), "", info)
	return nil
}

// assets is what a first page load of one built UI downloads: JS and CSS
// gzipped, fonts as shipped.
type assets struct {
	js        int64 // entry script and modulepreloads
	jsFiles   int
	css       int64 // stylesheets
	fonts     int64 // preloaded or declared by the stylesheets
	fontFiles int
}

// initialAssets sums the files dist/index.html loads up front. Fonts count
// once each, whether preloaded or declared in an initial stylesheet (an
// upper bound: a browser fetches a declared font only when text uses it).
func initialAssets(dist string) (assets, error) {
	index, err := os.ReadFile(filepath.Join(dist, "index.html"))
	if err != nil {
		return assets{}, fmt.Errorf("%w (run `make web` first)", err)
	}
	sum := func(re *regexp.Regexp) (int64, int, error) {
		var total int64
		ms := re.FindAllSubmatch(index, -1)
		for _, m := range ms {
			n, err := gzipSize(filepath.Join(dist, strings.TrimPrefix(string(m[1]), "/")))
			if err != nil {
				return 0, 0, err
			}
			total += n
		}
		return total, len(ms), nil
	}
	entry, entries, err := sum(entryRe)
	if err != nil {
		return assets{}, err
	}
	if entries == 0 {
		return assets{}, fmt.Errorf("no entry script in %s/index.html", dist)
	}
	preload, preloads, err := sum(preloadRe)
	if err != nil {
		return assets{}, err
	}
	css, _, err := sum(stylesheetRe)
	if err != nil {
		return assets{}, err
	}
	a := assets{js: entry + preload, jsFiles: entries + preloads, css: css}

	// Font URLs starting with / are from the dist root; others from the file
	// that names them.
	fonts := map[string]bool{}
	add := func(from, ref string) {
		if strings.HasPrefix(ref, "/") {
			fonts[filepath.Join(dist, ref)] = true
		} else {
			fonts[filepath.Join(from, ref)] = true
		}
	}
	for _, m := range fontPreloadRe.FindAllSubmatch(index, -1) {
		add(dist, string(m[1]))
	}
	for _, m := range stylesheetRe.FindAllSubmatch(index, -1) {
		sheet := filepath.Join(dist, strings.TrimPrefix(string(m[1]), "/"))
		b, err := readDist(sheet)
		if err != nil {
			return assets{}, err
		}
		for _, f := range fontURLRe.FindAllSubmatch(b, -1) {
			add(filepath.Dir(sheet), string(f[1]))
		}
	}
	for p := range fonts {
		st, err := os.Stat(p)
		if err != nil {
			return assets{}, err
		}
		a.fonts += st.Size()
		a.fontFiles++
	}
	return a, nil
}

// gzipSize is a built file's gzipped size: its .gz as the build wrote it
// (web/gzipDist.ts), else the file gzipped here.
func gzipSize(path string) (int64, error) {
	if st, err := os.Stat(path + ".gz"); err == nil {
		return st.Size(), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return int64(buf.Len()), nil
}

// readDist reads a built file, from its .gz when the build compressed it.
func readDist(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return b, err
	}
	f, err := os.Open(path + ".gz")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}
