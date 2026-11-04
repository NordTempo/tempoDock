package compose

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// TestGolden converts each testdata/*.yml and compares the project file,
// warnings, secret names and builds with its .golden file. The files a
// Compose file reads (.env, env_file entries) live in testdata/<name>/,
// keyed by their path relative to it. Run with -update to rewrite.
func TestGolden(t *testing.T) {
	files, err := filepath.Glob("testdata/*.yml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no testdata: %v", err)
	}
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".yml")
		t.Run(name, func(t *testing.T) {
			in := Input{Compose: mustRead(t, path), EnvFiles: readDir(t, filepath.Join("testdata", name))}
			res, err := Convert(in)
			if err != nil {
				t.Fatal(err)
			}
			got := render(t, res)
			golden := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			if want := mustRead(t, golden); !bytes.Equal(got, want) {
				t.Errorf("%s differs from the golden file (run with -update to accept):\n%s", name, got)
			}
			assertParses(t, res.File)
			assertNoSecretValues(t, res)
		})
	}
}

// render writes a result as the golden files hold it. Secret values are
// never written: only whether one is set.
func render(t *testing.T, res Result) []byte {
	t.Helper()
	file, err := res.File.TOML()
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	b.Write(file)
	b.WriteString("\n# warnings\n")
	for _, w := range res.Warnings {
		fmt.Fprintf(&b, "%s | %s | %s\n", w.Service, w.Key, w.Message)
	}
	b.WriteString("\n# secrets\n")
	for _, s := range res.Secrets {
		state := "(value set)"
		if s.Value == "" {
			state = "(needs a value)"
		}
		fmt.Fprintf(&b, "%s %s %s\n", s.Service, s.Key, state)
	}
	b.WriteString("\n# builds\n")
	for _, bl := range res.Builds {
		fmt.Fprintf(&b, "%s: context %s, dockerfile %s\n", bl.Service, bl.Context, bl.Dockerfile)
	}
	return b.Bytes()
}

// assertParses decodes the rendered project file strictly, as `dock apply`
// does, and checks it round-trips.
func assertParses(t *testing.T, ex apitypes.ProjectExport) {
	t.Helper()
	file, err := ex.TOML()
	if err != nil {
		t.Fatal(err)
	}
	var back apitypes.ProjectExport
	dec := toml.NewDecoder(bytes.NewReader(file))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("the project file doesn't parse back: %v", err)
	}
	again, err := back.TOML()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(file, again) {
		t.Errorf("the project file changes on a round trip:\n%s\n---\n%s", file, again)
	}
}

// assertNoSecretValues checks that no secret's value appears anywhere in
// the project file or in a warning.
func assertNoSecretValues(t *testing.T, res Result) {
	t.Helper()
	file, err := res.File.TOML()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Secrets {
		if s.Value == "" {
			continue
		}
		if bytes.Contains(file, []byte(s.Value)) {
			t.Errorf("the value of secret %s.%s is in the project file", s.Service, s.Key)
		}
		for _, w := range res.Warnings {
			if strings.Contains(w.Message, s.Value) {
				t.Errorf("the value of secret %s.%s is in a warning: %s", s.Service, s.Key, w.Message)
			}
		}
	}
}

// TestSecretsStayOutOfTheFile checks every way a secret reaches a Compose
// file: secret-looking keys, URLs with passwords, env files and .env.
func TestSecretsStayOutOfTheFile(t *testing.T) {
	res, err := Convert(Input{
		Compose: []byte(`
services:
  api:
    image: acme/api
    env_file: api.env
    environment:
      GITHUB_TOKEN: ghp_tokenvalue1
      AMQP: amqp://user:amqppass2@queue:5672/
      SIGNING_KEY: ${SIGNING_KEY}
      LISTEN: ":8080"
`),
		EnvFiles: map[string]string{
			".env":    "SIGNING_KEY=signingvalue3\n",
			"api.env": "DB_PASSWORD=filepass4\n",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecretValues(t, res)
	var keys []string
	for _, s := range res.Secrets {
		if s.Value == "" {
			t.Errorf("secret %s has no value", s.Key)
		}
		keys = append(keys, s.Key)
	}
	if want := []string{"AMQP", "DB_PASSWORD", "GITHUB_TOKEN", "SIGNING_KEY"}; !slices.Equal(keys, want) {
		t.Errorf("secrets %v, want %v", keys, want)
	}
	api := res.File.Environments[0].Services[0]
	if !slices.Equal(api.Variables, keys) || api.Values["LISTEN"] != ":8080" || len(api.Values) != 1 {
		t.Errorf("variables %v, values %v", api.Variables, api.Values)
	}
}

// TestConvertErrors checks the files Convert refuses.
func TestConvertErrors(t *testing.T) {
	for name, src := range map[string]string{
		"empty":       "",
		"no services": "name: x\nservices: {}\n",
		"not yaml":    "services: [\n",
		"a list":      "- a\n- b\n",
		"bad ports":   "services:\n  web:\n    image: x\n    ports: 80\n",
	} {
		if _, err := Convert(Input{Compose: []byte(src)}); err == nil {
			t.Errorf("%s: converted, want an error", name)
		}
	}
}

// TestNames checks Compose names becoming Dock names.
func TestNames(t *testing.T) {
	got := assignNames([]string{"My_App", "my-app", "shared", "x.y", "__", strings.Repeat("a", 40)}, "service", reservedNames)
	want := map[string]string{
		"My_App": "my-app-2", "my-app": "my-app", "shared": "shared-2", "x.y": "x-y", "__": "service",
		strings.Repeat("a", 40): strings.Repeat("a", 32),
	}
	for in, w := range want {
		if got[in] != w {
			t.Errorf("%q: got %q, want %q", in, got[in], w)
		}
	}
}

// mustRead reads a file or fails the test.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// readDir reads the files under dir, keyed by their path relative to it.
func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(mustRead(t, p))
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}
