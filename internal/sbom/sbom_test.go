package sbom

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A Syft JSON SBOM with a current package (objects for CPEs and
// licenses), an old one (strings) and what follows the artifacts.
const syftDoc = `{"artifacts":[
 {"id":"a1","name":"busybox","version":"1.36.1-r29","type":"apk","purl":"pkg:apk/alpine/busybox@1.36.1-r29",
  "cpes":[{"cpe":"cpe:2.3:a:busybox:busybox:1.36.1-r29:*:*:*:*:*:*:*","source":"x"}],
  "licenses":[{"value":"GPL-2.0-only","spdxExpression":"GPL-2.0-only","type":"declared"}]},
 {"id":"a2","name":"sharp","version":"0.33.1","type":"npm","purl":"pkg:npm/sharp@0.33.1",
  "cpes":["cpe:2.3:a:sharp:sharp:0.33.1:*:*:*:*:*:*:*"],"licenses":["Apache-2.0","MIT"]},
 {"id":"a3","name":"dual","version":"1","type":"npm","licenses":[{"value":"MIT OR Apache-2.0","spdxExpression":"MIT OR Apache-2.0"}]}
],"artifactRelationships":[],"source":{"type":"directory"},"distro":{"prettyName":"Alpine"},"descriptor":{"name":"syft"}}`

var img = Image{Name: "ghcr.io/acme/web:1.4", Digest: "sha256:abc", Distro: "alpine 3.20.3", Tool: "dock v1.2.3", Created: time.Unix(0, 0)}

func convertTo(t *testing.T, f Format) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := Convert(&out, strings.NewReader(syftDoc), f, img); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%s isn't JSON: %v\n%s", f, err, out.String())
	}
	return doc
}

func TestCycloneDX(t *testing.T) {
	doc := convertTo(t, CycloneDXJSON)
	if doc["bomFormat"] != "CycloneDX" || doc["specVersion"] != "1.6" || !strings.HasPrefix(doc["serialNumber"].(string), "urn:uuid:") {
		t.Fatalf("doc = %v", doc)
	}
	meta := doc["metadata"].(map[string]any)
	if c := meta["component"].(map[string]any); c["name"] != img.Name || c["type"] != "container" || c["version"] != "sha256:abc" {
		t.Fatalf("subject = %v", c)
	}
	comps := doc["components"].([]any)
	if len(comps) != 4 {
		t.Fatalf("%d components, want 3 packages and the OS: %v", len(comps), comps)
	}
	busybox := comps[0].(map[string]any)
	if busybox["purl"] != "pkg:apk/alpine/busybox@1.36.1-r29" || !strings.HasPrefix(busybox["cpe"].(string), "cpe:2.3:a:busybox") {
		t.Fatalf("busybox = %v", busybox)
	}
	want := map[int]string{
		0: `[{"license":{"id":"GPL-2.0-only"}}]`,
		1: `[{"license":{"name":"Apache-2.0"}},{"license":{"name":"MIT"}}]`,
		2: `[{"expression":"MIT OR Apache-2.0"}]`,
	}
	for i, w := range want {
		got, _ := json.Marshal(comps[i].(map[string]any)["licenses"])
		if string(got) != w {
			t.Errorf("component %d licenses = %s, want %s", i, got, w)
		}
	}
	if os := comps[3].(map[string]any); os["type"] != "operating-system" || os["name"] != "alpine" || os["version"] != "3.20.3" {
		t.Fatalf("os = %v", os)
	}
}

func TestSPDX(t *testing.T) {
	doc := convertTo(t, SPDXJSON)
	if doc["spdxVersion"] != "SPDX-2.3" || doc["SPDXID"] != "SPDXRef-DOCUMENT" || doc["dataLicense"] != "CC0-1.0" ||
		!strings.HasPrefix(doc["documentNamespace"].(string), "https://spdx.org/spdxdocs/ghcr.io-acme-web-1.4-") {
		t.Fatalf("doc = %v", doc)
	}
	pkgs := doc["packages"].([]any)
	if len(pkgs) != 4 || pkgs[0].(map[string]any)["SPDXID"] != "SPDXRef-Image" {
		t.Fatalf("packages = %v", pkgs)
	}
	declared := []string{"GPL-2.0-only", "NOASSERTION", "MIT OR Apache-2.0"}
	for i, w := range declared {
		if got := pkgs[i+1].(map[string]any)["licenseDeclared"]; got != w {
			t.Errorf("package %d declares %v, want %s", i+1, got, w)
		}
	}
	rels := doc["relationships"].([]any)
	if len(rels) != 4 || rels[3].(map[string]any)["relatedSpdxElement"] != "SPDXRef-Package-3" {
		t.Fatalf("relationships = %v", rels)
	}
}

func TestConvertErrors(t *testing.T) {
	var out bytes.Buffer
	if err := Convert(&out, strings.NewReader(`{"artifacts":[{"name":`), CycloneDXJSON, img); err == nil {
		t.Fatal("a cut-off SBOM converted")
	}
	if err := Convert(&out, strings.NewReader(syftDoc), "xml", img); err == nil {
		t.Fatal("an unknown format converted")
	}
	out.Reset()
	if err := Convert(&out, strings.NewReader(syftDoc), SyftJSON, img); err != nil || out.String() != syftDoc {
		t.Fatalf("syft-json isn't copied as it is: %v", err)
	}
}
