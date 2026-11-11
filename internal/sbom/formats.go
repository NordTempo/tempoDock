package sbom

import (
	"bufio"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// CycloneDX 1.6 (https://cyclonedx.org/docs/1.6/json/): the image is the
// metadata's component, each package a library, the operating system a
// component of its own.
type cycloneDX struct {
	img Image
	n   int
}

func newCycloneDX(img Image) *cycloneDX { return &cycloneDX{img: img} }

type cdxComponent struct {
	BOMRef   string       `json:"bom-ref,omitempty"`
	Type     string       `json:"type"`
	Name     string       `json:"name"`
	Version  string       `json:"version,omitempty"`
	PURL     string       `json:"purl,omitempty"`
	CPE      string       `json:"cpe,omitempty"`
	Licenses []cdxLicense `json:"licenses,omitempty"`
}

type cdxLicense struct {
	License    *cdxLicenseID `json:"license,omitempty"`
	Expression string        `json:"expression,omitempty"`
}

type cdxLicenseID struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func (c *cycloneDX) head(w *bufio.Writer) error {
	tool := strings.SplitN(c.img.Tool, " ", 2)
	tc := cdxComponent{Type: "application", Name: tool[0]}
	if len(tool) == 2 {
		tc.Version = tool[1]
	}
	meta := map[string]any{
		"timestamp": c.img.Created.UTC().Format(time.RFC3339),
		"tools":     map[string]any{"components": []cdxComponent{tc}},
		"component": cdxComponent{BOMRef: "image", Type: "container", Name: c.img.Name, Version: c.img.Digest},
	}
	_, _ = w.WriteString(`{"bomFormat":"CycloneDX","specVersion":"1.6","serialNumber":"urn:uuid:` + uuid() + `","version":1,"metadata":`)
	if err := writeJSON(w, meta); err != nil {
		return err
	}
	_, err := w.WriteString(`,"components":[`)
	return err
}

func (c *cycloneDX) pkg(w *bufio.Writer, a artifact) error {
	ref := a.ID
	if ref == "" {
		ref = "pkg-" + strconv.Itoa(c.n+1)
	}
	return c.component(w, cdxComponent{
		BOMRef: ref, Type: "library", Name: a.Name, Version: a.Version, PURL: a.PURL, CPE: a.cpe(), Licenses: cdxLicenses(a.licenses()),
	})
}

func (c *cycloneDX) component(w *bufio.Writer, comp cdxComponent) error {
	if c.n > 0 {
		_ = w.WriteByte(',')
	}
	c.n++
	return writeJSON(w, comp)
}

func (c *cycloneDX) tail(w *bufio.Writer) error {
	if name, version := distro(c.img.Distro); name != "" {
		if err := c.component(w, cdxComponent{BOMRef: "os", Type: "operating-system", Name: name, Version: version}); err != nil {
			return err
		}
	}
	_, err := w.WriteString("]}\n")
	return err
}

// cdxLicenses are licenses as CycloneDX takes them: SPDX IDs, names, or
// (when any is a compound expression) one expression, which CycloneDX
// allows only alone.
func cdxLicenses(ls []license) []cdxLicense {
	if len(ls) == 0 {
		return nil
	}
	if expr, ok := spdxExpression(ls); ok && strings.ContainsAny(expr, " ()") {
		return []cdxLicense{{Expression: expr}}
	}
	out := make([]cdxLicense, 0, len(ls))
	for _, l := range ls {
		switch {
		case l.spdx != "" && !strings.ContainsAny(l.spdx, " ()"):
			out = append(out, cdxLicense{License: &cdxLicenseID{ID: l.spdx}})
		case l.name != "":
			out = append(out, cdxLicense{License: &cdxLicenseID{Name: l.name}})
		default:
			out = append(out, cdxLicense{License: &cdxLicenseID{Name: l.spdx}})
		}
	}
	return out
}

// spdxExpression joins licenses into one SPDX expression (AND), when
// every one has an SPDX expression.
func spdxExpression(ls []license) (string, bool) {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		if l.spdx == "" {
			return "", false
		}
		p := l.spdx
		if len(ls) > 1 && strings.Contains(p, " ") {
			p = "(" + p + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " AND "), len(parts) > 0
}

// SPDX 2.3 (https://spdx.github.io/spdx-spec/v2.3/): the document
// describes the image's package, which contains every other.
type spdx struct {
	img Image
	n   int
}

func newSPDX(img Image) *spdx { return &spdx{img: img} }

type spdxPackage struct {
	Name             string         `json:"name"`
	SPDXID           string         `json:"SPDXID"`
	VersionInfo      string         `json:"versionInfo,omitempty"`
	DownloadLocation string         `json:"downloadLocation"`
	FilesAnalyzed    bool           `json:"filesAnalyzed"`
	LicenseConcluded string         `json:"licenseConcluded"`
	LicenseDeclared  string         `json:"licenseDeclared"`
	CopyrightText    string         `json:"copyrightText"`
	Purpose          string         `json:"primaryPackagePurpose,omitempty"`
	ExternalRefs     []spdxExtRef   `json:"externalRefs,omitempty"`
	Checksums        []spdxChecksum `json:"checksums,omitempty"`
}

type spdxExtRef struct {
	Category string `json:"referenceCategory"`
	Type     string `json:"referenceType"`
	Locator  string `json:"referenceLocator"`
}

type spdxChecksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"checksumValue"`
}

const noAssertion = "NOASSERTION"

func (s *spdx) head(w *bufio.Writer) error {
	doc := map[string]any{
		"spdxVersion":       "SPDX-2.3",
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              s.img.Name,
		"documentNamespace": "https://spdx.org/spdxdocs/" + spdxSafe(s.img.Name) + "-" + uuid(),
		"creationInfo": map[string]any{
			"created":  s.img.Created.UTC().Format(time.RFC3339),
			"creators": []string{"Tool: " + strings.ReplaceAll(s.img.Tool, " ", "-")},
		},
		"documentDescribes": []string{"SPDXRef-Image"},
	}
	// The head is the document without its closing brace, so packages and
	// relationships can follow as they stream.
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	head := strings.TrimSuffix(string(b), "}")
	_, _ = w.WriteString(head + `,"packages":[`)
	image := spdxPackage{
		Name: s.img.Name, SPDXID: "SPDXRef-Image", DownloadLocation: noAssertion, LicenseConcluded: noAssertion,
		LicenseDeclared: noAssertion, CopyrightText: noAssertion, Purpose: "CONTAINER",
	}
	if algo, value, ok := strings.Cut(s.img.Digest, ":"); ok && algo == "sha256" {
		image.VersionInfo = s.img.Digest
		image.Checksums = []spdxChecksum{{Algorithm: "SHA256", Value: value}}
	}
	return writeJSON(w, image)
}

func (s *spdx) pkg(w *bufio.Writer, a artifact) error {
	s.n++
	p := spdxPackage{
		Name: a.Name, SPDXID: "SPDXRef-Package-" + strconv.Itoa(s.n), VersionInfo: a.Version, DownloadLocation: noAssertion,
		LicenseConcluded: noAssertion, LicenseDeclared: noAssertion, CopyrightText: noAssertion, Purpose: "LIBRARY",
	}
	if expr, ok := spdxExpression(a.licenses()); ok {
		p.LicenseDeclared = expr
	}
	if a.PURL != "" {
		p.ExternalRefs = append(p.ExternalRefs, spdxExtRef{Category: "PACKAGE-MANAGER", Type: "purl", Locator: a.PURL})
	}
	if cpe := a.cpe(); cpe != "" {
		p.ExternalRefs = append(p.ExternalRefs, spdxExtRef{Category: "SECURITY", Type: "cpe23Type", Locator: cpe})
	}
	_ = w.WriteByte(',')
	return writeJSON(w, p)
}

func (s *spdx) tail(w *bufio.Writer) error {
	_, _ = w.WriteString(`],"relationships":[{"spdxElementId":"SPDXRef-DOCUMENT","relationshipType":"DESCRIBES","relatedSpdxElement":"SPDXRef-Image"}`)
	for i := 1; i <= s.n; i++ {
		_, _ = w.WriteString(`,{"spdxElementId":"SPDXRef-Image","relationshipType":"CONTAINS","relatedSpdxElement":"SPDXRef-Package-` + strconv.Itoa(i) + `"}`)
	}
	_, err := w.WriteString("]}\n")
	return err
}

// spdxSafe makes a name fit a namespace URI's path.
func spdxSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '-'
	}, s)
}
