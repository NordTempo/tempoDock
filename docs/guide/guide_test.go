package guide

import (
	"bufio"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// helpTopics is the web UI's help content; each topic links a guide page.
const helpTopics = "../../web/src/features/help/topics.ts"

// Every help topic's "Guide" link opens a page that exists, at a heading
// it has. The anchors follow the web guide reader's rule (slugOf).
func TestHelpTopicGuideLinks(t *testing.T) {
	src, err := os.ReadFile(helpTopics)
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`guide:\s*"([^"]*)"`).FindAllStringSubmatch(string(src), -1)
	if len(links) < 20 {
		t.Fatalf("found %d guide links in %s; did the format change?", len(links), helpTopics)
	}
	for _, m := range links {
		page, anchor, _ := strings.Cut(m[1], "#")
		b, err := fs.ReadFile(Pages, page+".md")
		if err != nil {
			t.Errorf("%s: no page %s.md", m[1], page)
			continue
		}
		if anchor != "" && !hasAnchor(string(b), anchor) {
			t.Errorf("%s: %s.md has no heading with that anchor", m[1], page)
		}
	}
}

// hasAnchor reports whether a heading of md (outside code blocks) slugs
// to anchor.
func hasAnchor(md, anchor string) bool {
	seen := map[string]int{}
	inCode := false
	sc := bufio.NewScanner(strings.NewReader(md))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
			continue
		}
		if inCode || !strings.HasPrefix(line, "#") {
			continue
		}
		// A repeated heading gets -1, -2, ... after its slug.
		slug := slugOf(strings.TrimLeft(line, "# "))
		n := seen[slug]
		seen[slug]++
		if n > 0 {
			slug += "-" + strconv.Itoa(n)
		}
		if slug == anchor {
			return true
		}
	}
	return false
}

var unslug = regexp.MustCompile(`[^a-z0-9 _-]`)

// slugOf is a heading's anchor the way GitHub and the web guide reader
// (features/help/markdown.tsx, slug) make it: lower case, without inline
// code marks and punctuation, spaces as hyphens.
func slugOf(heading string) string {
	s := strings.ToLower(strings.TrimSpace(heading))
	s = unslug.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, " ", "-")
}
