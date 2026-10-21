package clientconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.CurrentContext = "prod"
	c.Contexts["prod"] = Context{Server: "https://dock.example.com", Token: "secret"}
	if err := c.SetLink(t.TempDir(), Link{Context: "prod", Project: Ref{ID: "p1", Name: "shop"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", info.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Contexts["prod"].Token != "secret" || got.CurrentContext != "prod" || len(got.Links) != 1 {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestFindLink(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	for _, d := range []string{"app/web/src", "apple"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := &Config{}
	if err := c.SetLink(app, Link{Context: "prod"}); err != nil {
		t.Fatal(err)
	}
	wantDir, _ := canonical(app)

	if dir, _, ok := c.FindLink(filepath.Join(app, "web", "src")); !ok || dir != wantDir {
		t.Errorf("subdirectory should find the link at %s, got %q, %v", wantDir, dir, ok)
	}
	if _, _, ok := c.FindLink(filepath.Join(root, "apple")); ok {
		t.Error("a sibling sharing a name prefix must not match")
	}
	if _, _, ok := c.FindLink(root); ok {
		t.Error("a parent must not match")
	}

	// A symlinked path to the project finds the same link.
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(app, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.FindLink(alias); !ok {
		t.Error("a symlink to the linked dir should find the link")
	}

	if dir, _, ok := c.Unlink(filepath.Join(app, "web")); !ok || dir != wantDir || len(c.Links) != 0 {
		t.Errorf("unlink from a subdirectory should remove the nearest link, got %q, %v", dir, ok)
	}
}

func TestResolve(t *testing.T) {
	for _, k := range []string{EnvURL, EnvToken, EnvContext} {
		t.Setenv(k, "")
	}
	c := &Config{
		CurrentContext: "prod",
		Contexts: map[string]Context{
			"prod": {Server: "https://prod", Token: "prod-token"},
			"home": {Server: "http://home", Token: "home-token"},
		},
	}
	link := &Link{Context: "home"}

	check := func(name string, link *Link, want Resolved) {
		t.Helper()
		got, err := c.Resolve(link)
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", name, got, err, want)
		}
	}

	check("current", nil, Resolved{Name: "prod", Server: "https://prod", Token: "prod-token", Source: SourceCurrent})
	check("link beats current", link, Resolved{Name: "home", Server: "http://home", Token: "home-token", Source: SourceLink})

	t.Setenv(EnvContext, "prod")
	check("env context beats link", link, Resolved{Name: "prod", Server: "https://prod", Token: "prod-token", Source: SourceEnvContext})

	// A token without its URL is never sent to a context's server.
	t.Setenv(EnvToken, "env-token")
	if _, err := c.Resolve(link); !errors.Is(err, ErrTokenWithoutURL) {
		t.Errorf("token without URL: got %v", err)
	}

	t.Setenv(EnvURL, "https://ci")
	check("env URL and token go together", link, Resolved{Server: "https://ci", Token: "env-token", Source: SourceEnvURL})
	t.Setenv(EnvToken, "")
	check("env URL ignores contexts and their tokens", link, Resolved{Server: "https://ci", Source: SourceEnvURL})

	t.Setenv(EnvURL, "")
	t.Setenv(EnvContext, "missing")
	if _, err := c.Resolve(nil); !errors.Is(err, ErrContextNotFound) {
		t.Errorf("unknown context: got %v", err)
	}
	t.Setenv(EnvContext, "")
	if _, err := (&Config{}).Resolve(nil); !errors.Is(err, ErrNoContext) {
		t.Errorf("empty config: got %v", err)
	}
}
