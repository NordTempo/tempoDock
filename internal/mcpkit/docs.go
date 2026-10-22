package mcpkit

import (
	"bufio"
	"bytes"
	"io/fs"
	"regexp"
	"strings"
)

// Docs are a host's user guide as resources: every page as <URI><page>,
// so an agent can learn the product (and explain it to the user) from the
// server it talks to, matching that server's version. The guide's
// README.md is <URI>index. Pages are read from the embedded FS, not
// copied.
type Docs struct {
	// FS holds the guide's *.md pages at its root.
	FS fs.FS
	// URI prefixes page names: "dock://docs/".
	URI string
	// IndexDescription describes the README.md page.
	IndexDescription string
}

// doc is a guide page as resources/list shows it.
type doc struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType"`
	file        string
}

// load lists the guide's pages, with titles from their first heading and
// descriptions from the guide index's table.
func (d Docs) load() []doc {
	if d.FS == nil {
		return []doc{}
	}
	files, err := fs.Glob(d.FS, "*.md")
	if err != nil {
		panic(err) // a constant pattern
	}
	index, _ := fs.ReadFile(d.FS, "README.md")
	about := describe(index)
	out := make([]doc, 0, len(files))
	for _, file := range files {
		name := strings.TrimSuffix(file, ".md")
		desc := about[name]
		if name == "README" {
			name, desc = "index", d.IndexDescription
		}
		b, _ := fs.ReadFile(d.FS, file)
		out = append(out, doc{URI: d.URI + name, Name: name, Title: heading(b, name), Description: desc, MIMEType: "text/markdown", file: file})
	}
	return out
}

// read answers resources/read.
func (d Docs) read(docs []doc, uri string) (any, error) {
	for _, page := range docs {
		if page.URI != uri {
			continue
		}
		b, err := fs.ReadFile(d.FS, page.file)
		if err != nil {
			return nil, err
		}
		return map[string]any{"contents": []map[string]string{{"uri": uri, "mimeType": page.MIMEType, "text": string(b)}}}, nil
	}
	return nil, &rpcError{codeNoResource, "resource " + uri + " not found; resources/list shows the guide's pages"}
}

// describe reads each page's one-line description from the guide index's
// table rows: | [Title](page.md) | For |. It runs once, on the first
// request, so the pattern isn't compiled at start-up.
func describe(index []byte) map[string]string {
	row := regexp.MustCompile(`^\| \[[^\]]+\]\(([a-z0-9-]+)\.md\) \| (.+) \|$`)
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(index))
	for sc.Scan() {
		if m := row.FindStringSubmatch(sc.Text()); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

// heading is a page's first "# " heading, or def.
func heading(page []byte, def string) string {
	sc := bufio.NewScanner(bytes.NewReader(page))
	for sc.Scan() {
		if t, ok := strings.CutPrefix(sc.Text(), "# "); ok {
			return t
		}
	}
	return def
}
