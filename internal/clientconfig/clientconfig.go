// Package clientconfig is the CLI's user config file: contexts (which server
// to talk to, with which token) and directory links (which project,
// environment and service a directory deploys to).
//
// The file lives at $XDG_CONFIG_HOME/dock/config.json (default
// ~/.config/dock/config.json) and holds tokens, so it is written with
// mode 0600 and replaced atomically. Links live here rather than in the repo,
// so cloning a repo never points you at someone else's server.
package clientconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Environment variables that override the config file. With DOCK_URL
// set, no config file is needed at all (agents, CI).
const (
	EnvContext = "DOCK_CONTEXT" // selects a context by name
	EnvURL     = "DOCK_URL"     // server URL; ignores contexts entirely
	EnvToken   = "DOCK_TOKEN"   // API token for DOCK_URL; only valid with it
)

// Errors returned by Resolve.
var (
	ErrNoContext       = errors.New("no context configured")
	ErrContextNotFound = errors.New("context not found")
	// ErrTokenWithoutURL means DOCK_TOKEN is set without DOCK_URL. The
	// token is never paired with a context's server, which it may not
	// belong to.
	ErrTokenWithoutURL = errors.New(EnvToken + " is set without " + EnvURL)
)

// Config is the whole file.
type Config struct {
	CurrentContext string             `json:"currentContext,omitempty"`
	Contexts       map[string]Context `json:"contexts"`
	// Links maps an absolute directory path to what it deploys to.
	Links map[string]Link `json:"links"`
}

// Context is one server the CLI can talk to.
type Context struct {
	Server string `json:"server"`
	Token  string `json:"token,omitempty"`
	// LoginToken marks a token that `dock login` got in the browser. It is
	// this machine's own, so logging out revokes it on the server.
	LoginToken bool `json:"loginToken,omitempty"`
}

// Link is what a directory is linked to with `dock link`.
type Link struct {
	Context     string `json:"context"`
	Project     Ref    `json:"project"`
	Environment Ref    `json:"environment"`
	// Service is empty for a directory linked to a whole environment.
	Service Ref `json:"service,omitzero"`
}

// Ref names a platform object by ID (stable) and name (for display).
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DefaultPath returns $XDG_CONFIG_HOME/dock/config.json, falling back to
// ~/.config on every OS so the location is the same on Linux and macOS.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "dock", "config.json"), nil
}

// Load reads the config at path. A missing file is an empty config.
func Load(path string) (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	if c.Links == nil {
		c.Links = map[string]Link{}
	}
	return c, nil
}

// Save writes the config to path with mode 0600, atomically: readers see
// either the old or the new file, never a partial one.
func (c *Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json") // created with mode 0600
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(b, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// RemoveContext deletes a context, clearing CurrentContext if it pointed
// there. It reports whether the context existed. Links to it are kept, so
// re-adding a context with the same name brings them back.
func (c *Config) RemoveContext(name string) bool {
	if _, ok := c.Contexts[name]; !ok {
		return false
	}
	delete(c.Contexts, name)
	if c.CurrentContext == name {
		c.CurrentContext = ""
	}
	return true
}

// Source says where a resolved context came from.
type Source string

const (
	SourceEnvURL     Source = EnvURL
	SourceEnvContext Source = EnvContext
	SourceLink       Source = "link"
	SourceCurrent    Source = "current"
)

// Resolved is the server and token a command should use.
type Resolved struct {
	// Name is the context name; empty when DOCK_URL is in use.
	Name   string
	Server string
	// Token is empty when none is configured.
	Token  string
	Source Source
}

// Resolve picks the server and token to use, in order:
//
//  1. DOCK_URL with DOCK_TOKEN: contexts are ignored, so a token from
//     the config file is never sent to a server it wasn't made for.
//  2. The context named by DOCK_CONTEXT.
//  3. The context of link, when the directory is linked (link may be nil).
//  4. The current context.
//
// A token always travels with the server it was given for: DOCK_TOKEN
// without DOCK_URL is ErrTokenWithoutURL rather than being sent to
// whichever context's server is picked. Other errors wrap ErrNoContext or
// ErrContextNotFound.
func (c *Config) Resolve(link *Link) (Resolved, error) {
	if u := os.Getenv(EnvURL); u != "" {
		return Resolved{Server: u, Token: os.Getenv(EnvToken), Source: SourceEnvURL}, nil
	}
	if os.Getenv(EnvToken) != "" {
		return Resolved{}, ErrTokenWithoutURL
	}
	name, src := os.Getenv(EnvContext), SourceEnvContext
	if name == "" && link != nil {
		name, src = link.Context, SourceLink
	}
	if name == "" {
		name, src = c.CurrentContext, SourceCurrent
	}
	if name == "" {
		return Resolved{}, ErrNoContext
	}
	ctx, ok := c.Contexts[name]
	if !ok {
		return Resolved{}, fmt.Errorf("%w: %q (from %s)", ErrContextNotFound, name, src)
	}
	return Resolved{Name: name, Server: ctx.Server, Token: ctx.Token, Source: src}, nil
}

// SetLink links dir (and everything below it) to l.
func (c *Config) SetLink(dir string, l Link) error {
	dir, err := canonical(dir)
	if err != nil {
		return err
	}
	if c.Links == nil {
		c.Links = map[string]Link{}
	}
	c.Links[dir] = l
	return nil
}

// FindLink returns the link that applies to dir: its own, or the nearest
// linked ancestor's, like git finds its repo. linkedDir is the directory the
// link is stored under.
func (c *Config) FindLink(dir string) (linkedDir string, l Link, ok bool) {
	dir, err := canonical(dir)
	if err != nil {
		return "", Link{}, false
	}
	for {
		if l, ok := c.Links[dir]; ok {
			return dir, l, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", Link{}, false
		}
		dir = parent
	}
}

// Unlink removes the link that applies to dir (see FindLink) and returns it.
func (c *Config) Unlink(dir string) (linkedDir string, l Link, ok bool) {
	linkedDir, l, ok = c.FindLink(dir)
	if ok {
		delete(c.Links, linkedDir)
	}
	return linkedDir, l, ok
}

// canonical makes dir absolute and resolves symlinks, so a project reached
// through a symlink (or macOS's /tmp → /private/tmp) finds the same link.
func canonical(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	return abs, nil
}
