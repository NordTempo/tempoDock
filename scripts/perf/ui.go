package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Assets index.html loads up front: the entry script, the chunks Vite
// modulepreloads, and stylesheets.
var (
	entryRe      = regexp.MustCompile(`<script[^>]*type="module"[^>]*src="([^"]+)"`)
	preloadRe    = regexp.MustCompile(`<link[^>]*rel="modulepreload"[^>]*href="([^"]+)"`)
	stylesheetRe = regexp.MustCompile(`<link[^>]*rel="stylesheet"[^>]*href="([^"]+)"`)
)

// checkUI measures what a first page load downloads: initial JS and CSS
// gzipped (as a browser receives them compressed), and fonts as shipped.
func checkUI(o options, t *table) error {
	index, err := os.ReadFile(filepath.Join(o.dist, "index.html"))
	if err != nil {
		return fmt.Errorf("%w (run `make web` first)", err)
	}
	sum := func(re *regexp.Regexp) (int64, int, error) {
		var total int64
		ms := re.FindAllSubmatch(index, -1)
		for _, m := range ms {
			n, err := gzipSize(filepath.Join(o.dist, strings.TrimPrefix(string(m[1]), "/")))
			if err != nil {
				return 0, 0, err
			}
			total += n
		}
		return total, len(ms), nil
	}
	entry, entries, err := sum(entryRe)
	if err != nil {
		return err
	}
	if entries == 0 {
		return fmt.Errorf("no entry script in %s/index.html", o.dist)
	}
	preload, preloads, err := sum(preloadRe)
	if err != nil {
		return err
	}
	css, _, err := sum(stylesheetRe)
	if err != nil {
		return err
	}
	t.within("UI initial JS (gzip)", fmt.Sprintf("%s (%d files)", kb(entry+preload), entries+preloads), "≤ "+kb(initialJSBudget), entry+preload <= initialJSBudget)
	t.within("UI initial CSS (gzip)", kb(css), "≤ "+kb(cssBudget), css <= cssBudget)

	var fonts, allJS int64
	var fontFiles, jsFiles int
	err = filepath.WalkDir(o.dist, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		switch filepath.Ext(p) {
		case ".woff2", ".woff", ".ttf", ".otf":
			info, err := e.Info()
			if err != nil {
				return err
			}
			fonts += info.Size()
			fontFiles++
		case ".js":
			n, err := gzipSize(p)
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

func gzipSize(path string) (int64, error) {
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
