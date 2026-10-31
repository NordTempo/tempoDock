package ingest

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Rules are what each site's ingest leaves out (ADR 0037): excluded paths
// and ignored client addresses, set by the control plane with the salts
// and kept in a file, so a restarted server applies them at once.
type Rules struct {
	path  string
	sites atomic.Pointer[map[int64]siteRules]
}

// siteRules are one site's rules, ready to match.
type siteRules struct {
	paths []string
	nets  []netip.Prefix
}

// OpenRules reads the rules kept at path (none if it doesn't exist).
func OpenRules(path string) (*Rules, error) {
	r := &Rules{path: path}
	empty := map[int64]siteRules{}
	r.sites.Store(&empty)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var stored map[int64]node.SiteRules
	if err := json.Unmarshal(b, &stored); err != nil {
		return nil, err
	}
	r.load(stored)
	return r, nil
}

// Set replaces every site's rules and keeps them.
func (r *Rules) Set(sites map[int64]node.SiteRules) error {
	b, err := json.Marshal(sites)
	if err != nil {
		return err
	}
	if r.path != "" {
		if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
			return err
		}
		tmp := r.path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, r.path); err != nil {
			return err
		}
	}
	r.load(sites)
	return nil
}

// load compiles rules for matching.
func (r *Rules) load(sites map[int64]node.SiteRules) {
	m := make(map[int64]siteRules, len(sites))
	for id, s := range sites {
		var sr siteRules
		sr.paths = s.ExcludedPaths
		for _, ip := range s.IgnoredIPs {
			if p, err := netip.ParsePrefix(ip); err == nil {
				sr.nets = append(sr.nets, p.Masked())
			} else if a, err := netip.ParseAddr(ip); err == nil {
				sr.nets = append(sr.nets, netip.PrefixFrom(a, a.BitLen()))
			}
		}
		m[id] = sr
	}
	r.sites.Store(&m)
}

// skip reports whether a site's rules leave out an event on path from ip.
// No rules is a map lookup.
func (r *Rules) skip(site int64, path, ip string) bool {
	if r == nil {
		return false
	}
	sr, ok := (*r.sites.Load())[site]
	if !ok {
		return false
	}
	for _, p := range sr.paths {
		if globMatch(p, path) {
			return true
		}
	}
	if len(sr.nets) > 0 {
		if a, err := netip.ParseAddr(ip); err == nil {
			a = a.Unmap()
			for _, n := range sr.nets {
				if n.Contains(a) {
					return true
				}
			}
		}
	}
	return false
}

// globMatch matches s against pattern, where * matches any run of
// characters (slashes included) and everything else itself.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last)
}
