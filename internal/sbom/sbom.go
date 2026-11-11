// Package sbom converts the SBOMs Dock keeps (Syft's JSON, ADR 0074) to
// the two standard formats tools expect, CycloneDX 1.6 and SPDX 2.3, both
// JSON. It streams: one package is read and written at a time, so a
// conversion holds one package in memory however large the SBOM. Only
// the packages are converted (names, versions, purls, CPEs, licenses);
// file listings and relationships between packages are left out, as
// Dock's SBOMs are made without file metadata.
package sbom

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Format is an SBOM format Dock exports.
type Format string

// Formats.
const (
	SyftJSON      Format = "syft-json"
	CycloneDXJSON Format = "cyclonedx-json"
	SPDXJSON      Format = "spdx-json"
)

// Formats are the formats Dock exports, the stored one first.
var Formats = []Format{SyftJSON, CycloneDXJSON, SPDXJSON}

// Image describes the image an SBOM is of, for the document's subject:
// its name as Dock shows it, its digest (empty when unknown) and its
// operating system ("alpine 3.20.3", empty for none).
type Image struct {
	Name, Digest, Distro string
	// Tool names the tool writing the document, "dock v0.45.0".
	Tool string
	// Created is the document's time.
	Created time.Time
}

// artifact is what is converted of one of Syft's packages.
type artifact struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	Type     string          `json:"type"`
	PURL     string          `json:"purl"`
	CPEs     json.RawMessage `json:"cpes"`
	Licenses json.RawMessage `json:"licenses"`
}

// cpe is the first CPE: Syft's schema has them as objects ({"cpe": ...})
// since v13 and as strings before.
func (a artifact) cpe() string {
	var objs []struct {
		CPE string `json:"cpe"`
	}
	if json.Unmarshal(a.CPEs, &objs) == nil && len(objs) > 0 {
		return objs[0].CPE
	}
	var strs []string
	if json.Unmarshal(a.CPEs, &strs) == nil && len(strs) > 0 {
		return strs[0]
	}
	return ""
}

// licenses are the package's licenses: an SPDX expression when Syft found
// one, else the value as written. Older schemas list plain strings.
func (a artifact) licenses() []license {
	var objs []struct {
		Value string `json:"value"`
		SPDX  string `json:"spdxExpression"`
	}
	var out []license
	if json.Unmarshal(a.Licenses, &objs) == nil {
		for _, o := range objs {
			if o.SPDX != "" || o.Value != "" {
				out = append(out, license{spdx: o.SPDX, name: o.Value})
			}
		}
		return out
	}
	var strs []string
	if json.Unmarshal(a.Licenses, &strs) == nil {
		for _, s := range strs {
			out = append(out, license{name: s})
		}
	}
	return out
}

// license is one license: its SPDX expression, or a name.
type license struct{ spdx, name string }

// Convert reads a Syft JSON SBOM from r and writes it to w as format.
// SyftJSON copies it as it is.
func Convert(w io.Writer, r io.Reader, format Format, img Image) error {
	switch format {
	case SyftJSON, "":
		_, err := io.Copy(w, r)
		return err
	case CycloneDXJSON:
		return convert(w, r, newCycloneDX(img))
	case SPDXJSON:
		return convert(w, r, newSPDX(img))
	}
	return fmt.Errorf("unknown SBOM format %q: %s", format, formatList())
}

// Valid reports whether f is a format Convert writes.
func Valid(f Format) bool {
	for _, x := range Formats {
		if f == x {
			return true
		}
	}
	return false
}

func formatList() string {
	names := make([]string, len(Formats))
	for i, f := range Formats {
		names[i] = string(f)
	}
	return "use " + strings.Join(names, ", ")
}

// writer writes one output format: a head, each package, a tail.
type writer interface {
	head(w *bufio.Writer) error
	pkg(w *bufio.Writer, a artifact) error
	tail(w *bufio.Writer) error
}

// convert streams r's artifacts through f. The document's head is
// written first, so the subject comes from img, not from the SBOM's own
// source and distro (which Syft writes after the artifacts).
func convert(out io.Writer, r io.Reader, f writer) error {
	w := bufio.NewWriter(out)
	if err := f.head(w); err != nil {
		return err
	}
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return readErr(err)
		}
		if key != "artifacts" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return readErr(err)
			}
			continue
		}
		if err := expectDelim(dec, '['); err != nil {
			return err
		}
		for dec.More() {
			var a artifact
			if err := dec.Decode(&a); err != nil {
				return readErr(err)
			}
			if err := f.pkg(w, a); err != nil {
				return err
			}
		}
		// Nothing after the artifacts is converted.
		break
	}
	if err := f.tail(w); err != nil {
		return err
	}
	return w.Flush()
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return readErr(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("read the SBOM: want %q, found %v", want, tok)
	}
	return nil
}

func readErr(err error) error {
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("read the SBOM: %w", err)
}

// writeJSON writes v as JSON, without HTML escaping.
func writeJSON(w *bufio.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v) // a trailing newline is valid JSON whitespace
}

// uuid is a random version 4 UUID.
func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// distro splits "alpine 3.20.3" into its name and version.
func distro(s string) (name, version string) {
	name, version, _ = strings.Cut(strings.TrimSpace(s), " ")
	return name, version
}
