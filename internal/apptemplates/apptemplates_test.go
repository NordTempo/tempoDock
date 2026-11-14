package apptemplates

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// TestCatalog checks every built-in template: it parses, its images are
// pinned by digest, and it expands with its defaults and placeholder
// inputs into services whose references name each other.
func TestCatalog(t *testing.T) {
	cat, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat) < 20 {
		t.Fatalf("catalog has %d templates", len(cat))
	}
	for name, tm := range cat {
		for _, s := range tm.Services {
			if s.Image != "" && (!strings.Contains(s.Image, "@sha256:") || s.Track == "") {
				t.Errorf("%s/%s: image %s isn't pinned with a track; run go run ./scripts/templates pin %s", name, s.Name, s.Image, name)
			}
		}
		inputs := map[string]string{}
		for _, in := range tm.Inputs {
			switch in.Kind {
			case apitypes.InputEmail:
				inputs[in.Key] = "me@example.com"
			default:
				inputs[in.Key] = "x"
			}
			if in.Kind == apitypes.InputChoice {
				inputs[in.Key] = in.Default
			}
		}
		ex, err := tm.Expand("production", Use{Name: "app1", Inputs: inputs})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if ex.Services[0].Name != "app1" {
			t.Errorf("%s: main service is %s, not first", name, ex.Services[0].Name)
		}
		names := map[string]bool{}
		for _, s := range ex.Services {
			names[s.Name] = true
		}
		for _, s := range ex.Services {
			for k, v := range s.Values {
				for _, m := range refRe.FindAllStringSubmatch(v, -1) {
					if !names[m[1]] {
						t.Errorf("%s: %s.%s references %s", name, s.Name, k, m[0])
					}
				}
			}
		}
	}
}

// TestExpand covers naming, references, inputs and generated values.
func TestExpand(t *testing.T) {
	tm, err := Parse([]byte(`
name = "demo"
title = "Demo"
summary = "s"
category = "dev"
license = "MIT"
revision = 1
main = "web"
domain = true

[[inputs]]
  key = "ADMIN_EMAIL"
  label = "Admin email"
  kind = "email"

[[inputs]]
  key = "TOKEN"
  label = "Token"
  kind = "secret"
  service = "worker"

[[services]]
  name = "web"
  image = "demo:1"
  depends_on = ["db"]
  generate = { KEY = "hex:16" }
  [services.values]
    DATABASE_URL = "${{db.DATABASE_URL}}?x=1"
    SELF = "https://${{web.DOCK_PUBLIC_DOMAIN}}"
    SHARED = "${{shared.LOG_LEVEL}}"

[[services]]
  name = "worker"
  image = "demo:1"
  [services.values]
    KEY = "${{web.KEY}}"

[[services]]
  name = "db"
  template = "postgres"
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.Expand("prod", Use{Name: "shop"}); err == nil || !strings.Contains(err.Error(), "admin email") {
		t.Fatalf("missing input: %v", err)
	}
	if _, err := tm.Expand("prod", Use{Name: "shop", Inputs: map[string]string{"NOPE": "x"}}); err == nil || !strings.Contains(err.Error(), "its inputs are ADMIN_EMAIL, TOKEN") {
		t.Fatalf("unknown input: %v", err)
	}
	ex, err := tm.Expand("prod", Use{Name: "shop", Domain: "shop.example.com", Inputs: map[string]string{"ADMIN_EMAIL": "a@b.c", "TOKEN": "t0k"}})
	if err != nil {
		t.Fatal(err)
	}
	web, worker, db := ex.Services[0], ex.Services[1], ex.Services[2]
	if web.Name != "shop" || worker.Name != "shop-worker" || db.Name != "shop-db" || db.Template != apitypes.TemplatePostgres {
		t.Fatalf("names: %s %s %s", web.Name, worker.Name, db.Name)
	}
	want := map[string]string{
		"DATABASE_URL": "${{shop-db.DATABASE_URL}}?x=1", "SELF": "https://${{shop.DOCK_PUBLIC_DOMAIN}}",
		"SHARED": "${{shared.LOG_LEVEL}}", "ADMIN_EMAIL": "a@b.c",
	}
	for k, v := range want {
		if web.Values[k] != v {
			t.Errorf("%s = %q, want %q", k, web.Values[k], v)
		}
	}
	if worker.Values["KEY"] != "${{shop.KEY}}" || web.DependsOn[0] != "shop-db" {
		t.Errorf("worker KEY %q, depends_on %v", worker.Values["KEY"], web.DependsOn)
	}
	if len(web.Domains) != 1 || web.Domains[0].Hostname != "shop.example.com" {
		t.Errorf("domains %v", web.Domains)
	}
	// Generated and secret values are sent with the file, never in it.
	got := map[string]string{}
	for _, v := range ex.Values {
		got[v.Service+"."+v.Key] = v.Value
	}
	if len(got["shop.KEY"]) != 32 || got["shop-worker.TOKEN"] != "t0k" || len(got) != 2 {
		t.Errorf("values %v", got)
	}
	if web.Variables[0] != "KEY" || worker.Variables[0] != "TOKEN" {
		t.Errorf("variables %v %v", web.Variables, worker.Variables)
	}
}

// TestParseRefuses covers the checks a hand-written (org) template meets.
func TestParseRefuses(t *testing.T) {
	head := "name = \"x\"\ntitle = \"X\"\nsummary = \"s\"\ncategory = \"c\"\nlicense = \"MIT\"\nrevision = 1\nmain = \"app\"\n"
	for want, body := range map[string]string{
		"unknown keys":          "colour = 1\n[[services]]\nname = \"app\"\nimage = \"a\"\n",
		"isn't a service":       "[[services]]\nname = \"app\"\nimage = \"a\"\nvalues = { U = \"${{db.URL}}\" }\n",
		"secrets are inputs":    "[[services]]\nname = \"app\"\nimage = \"a\"\nvariables = [\"K\"]\n",
		"the kinds are":         "[[services]]\nname = \"app\"\nimage = \"a\"\ngenerate = { K = \"words\" }\n",
		"domains come from":     "[[services]]\nname = \"app\"\nimage = \"a\"\ndomains = [{ hostname = \"a.b\" }]\n",
		"needs an image":        "[[services]]\nname = \"app\"\n",
		"isn't in the template": "[[inputs]]\nkey = \"K\"\nlabel = \"K\"\nkind = \"text\"\nservice = \"db\"\n[[services]]\nname = \"app\"\nimage = \"a\"\n",
	} {
		if _, err := Parse([]byte(head + body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}
