package mcp

import (
	"bufio"
	"bytes"
	"io/fs"
	"regexp"
	"strings"

	"github.com/NordTempo/tempoDock/docs/guide"
)

// Docs resources: every user guide page as dock://docs/<page>, so an agent
// can learn the platform (and explain it to the user) from the server it
// talks to, matching that server's version. The guide's README is
// dock://docs/index. Pages are embedded in the binary (docs/guide), not
// copied.

const docsURI = "dock://docs/"

// doc is a guide page as resources/list shows it.
type doc struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType"`
	file        string
}

// loadDocs lists the guide's pages, with titles from their first heading
// and descriptions from the guide index's table.
func loadDocs() []doc {
	files, err := fs.Glob(guide.Pages, "*.md")
	if err != nil {
		panic(err) // a constant pattern
	}
	index, _ := fs.ReadFile(guide.Pages, "README.md")
	about := describe(index)
	out := make([]doc, 0, len(files))
	for _, file := range files {
		name := strings.TrimSuffix(file, ".md")
		desc := about[name]
		if name == "README" {
			name, desc = "index", "Index of the Tempo Dock guide and the words it uses (project, environment, service, release, ...)."
		}
		b, _ := fs.ReadFile(guide.Pages, file)
		out = append(out, doc{URI: docsURI + name, Name: name, Title: heading(b, name), Description: desc, MIMEType: "text/markdown", file: file})
	}
	return out
}

// readDoc answers resources/read.
func readDoc(docs []doc, uri string) (any, error) {
	for _, d := range docs {
		if d.URI != uri {
			continue
		}
		b, err := fs.ReadFile(guide.Pages, d.file)
		if err != nil {
			return nil, err
		}
		return map[string]any{"contents": []map[string]string{{"uri": uri, "mimeType": d.MIMEType, "text": string(b)}}}, nil
	}
	return nil, &rpcError{codeNoResource, "resource " + uri + " not found; resources/list shows the guide's pages"}
}

// describe reads each page's one-line description from the guide index's
// table rows: | [Title](page.md) | For |.
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
