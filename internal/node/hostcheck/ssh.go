package hostcheck

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/NordTempo/tempoDock/internal/node"
)

// sshdConfig is where OpenSSH's server reads its settings.
const sshdConfig = "/etc/ssh/sshd_config"

// sshSettings is what the SSH checks read from sshd's config: the first
// value of each keyword (sshd keeps the first one), with the file that set
// it, in the global section and in `Match all` blocks, which apply to
// every connection and so override the global section.
type sshSettings struct {
	// installed is false without an sshd_config: no SSH server.
	installed bool
	// unreadable is the first file that couldn't be read; the checks
	// that depend on what it might say can't tell.
	unreadable string
	values     map[string]sshValue
	// all are the values set under `Match all`.
	all map[string]sshValue
	// ports are the ports sshd listens on (see readSSH).
	ports []int
	// portLines are the Port lines' ports, listenPorts those of
	// ListenAddress host:port lines, and bareListen whether a ListenAddress
	// names no port (it listens on the Port lines', or 22).
	portLines, listenPorts []int
	bareListen, listens    bool
}

// sshValue is one keyword's value and where it was set.
type sshValue struct{ value, file string }

// readSSH reads sshd_config and the files it includes. Match blocks with
// criteria are left out: they apply to some connections only, and the
// checks report what anyone connecting gets, not what a matched user or
// address does.
func (c Checker) readSSH() sshSettings {
	s := sshSettings{values: map[string]sshValue{}, all: map[string]sshValue{}}
	// Missing is no SSH server; anything else (a directory Dock can't
	// enter) is one Dock can't read.
	if _, err := c.stat(sshdConfig); isNotExist(err) {
		return s
	}
	s.installed = true
	c.readSSHFile(&s, sshdConfig, s.values, 0)
	// A ListenAddress with a port listens there; one without, or none at
	// all, listens on the Port lines' ports, 22 without any.
	s.ports = s.listenPorts
	if !s.listens || s.bareListen {
		def := s.portLines
		if len(def) == 0 {
			def = []int{22}
		}
		s.ports = append(slices.Clone(s.ports), def...)
	}
	slices.Sort(s.ports)
	s.ports = slices.Compact(s.ports)
	return s
}

// readSSHFile reads one config file into s, following Include. into is
// the section it starts in: the global one, or for a file included under
// `Match all` that block's (and nil under a Match with criteria), as sshd
// reads an Include in place.
func (c Checker) readSSHFile(s *sshSettings, file string, into map[string]sshValue, depth int) {
	if depth > 8 {
		return
	}
	data, err := c.read(file)
	if err != nil {
		if s.unreadable == "" {
			s.unreadable = file
		}
		return
	}
	// into is where this file's values go: the global section, `Match
	// all`'s, or nil inside a Match block with criteria.
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, value := sshLine(sc.Text())
		switch {
		case key == "":
			continue
		case key == "match":
			into = nil
			if strings.EqualFold(value, "all") {
				into = s.all
			}
			continue
		case into == nil:
			continue
		case key == "include":
			for _, pattern := range sshArgs(value) {
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join("/etc/ssh", pattern)
				}
				matches, unreadable := c.glob(pattern)
				if unreadable != "" && s.unreadable == "" {
					s.unreadable = unreadable
				}
				for _, m := range matches {
					c.readSSHFile(s, m, into, depth+1)
				}
			}
			continue
		case key == "port":
			if p, err := strconv.Atoi(firstWord(value)); err == nil {
				s.portLines = append(s.portLines, p)
			}
			continue
		case key == "listenaddress":
			s.listens = true
			// "host:port" or "[v6]:port" listens on that port; a bare
			// address on the Port lines' (SplitHostPort refuses it).
			n := 0
			if _, p, err := net.SplitHostPort(firstWord(value)); err == nil {
				n, _ = strconv.Atoi(p)
			}
			if n > 0 {
				s.listenPorts = append(s.listenPorts, n)
			} else {
				s.bareListen = true
			}
			continue
		case key == "challengeresponseauthentication":
			key = "kbdinteractiveauthentication" // the old name
		}
		if _, set := into[key]; !set {
			v := firstWord(value)
			if key == "authenticationmethods" {
				v = value // a list of method lists
			}
			into[key] = sshValue{value: strings.ToLower(v), file: file}
		}
	}
}

// glob expands an Include pattern on the host one path element at a
// time, as filepath.Glob does, but says which directory it couldn't read
// (filepath.Glob skips those silently): a root-only sshd_config.d, or
// one a wildcard reached. Matches are host paths, sorted.
func (c Checker) glob(pattern string) (matches []string, unreadable string) {
	paths := []string{"/"}
	for _, elem := range strings.Split(strings.Trim(filepath.Clean(pattern), "/"), "/") {
		var next []string
		for _, dir := range paths {
			if !strings.ContainsAny(elem, `*?[\`) {
				_, err := os.Lstat(c.path(filepath.Join(dir, elem)))
				switch {
				case err == nil:
					next = append(next, filepath.Join(dir, elem))
				case !isNotExist(err) && !errors.Is(err, syscall.ENOTDIR) && unreadable == "":
					unreadable = dir // a directory on the way it can't enter
				}
				continue
			}
			entries, err := os.ReadDir(c.path(dir))
			if err != nil {
				// A file where a wildcard expects a directory matches nothing.
				if !isNotExist(err) && !errors.Is(err, syscall.ENOTDIR) && unreadable == "" {
					unreadable = dir
				}
				continue
			}
			for _, e := range entries {
				if ok, _ := filepath.Match(elem, e.Name()); ok {
					next = append(next, filepath.Join(dir, e.Name()))
				}
			}
		}
		paths = next
	}
	slices.Sort(paths)
	return paths, unreadable
}

// sshLine splits a config line into its lower-case keyword and value
// ("Keyword value" or "Keyword=value"), without a trailing comment as
// sshd reads it (a # outside quotes); comments and blanks are "".
func sshLine(line string) (key, value string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", ""
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), ""
	}
	value = strings.TrimLeft(line[i:], " \t=")
	// A # starts a comment only where a token would start: after a
	// space, outside quotes (10#site.conf is a file name).
	var quote rune // the quote open, '"' or '\'', or 0
	escaped := false
	for j, r := range value {
		if escaped {
			escaped = false // a backslash takes the next character as it is
			continue
		}
		if r == '\\' {
			escaped = true
		} else if (r == '"' || r == '\'') && (quote == 0 || quote == r) {
			quote = r ^ quote // opens, or closes the one open
		} else if r == '#' && quote == 0 && (j == 0 || value[j-1] == ' ' || value[j-1] == '\t') {
			value = value[:j]
			break
		}
	}
	return strings.ToLower(line[:i]), strings.TrimSpace(value)
}

// sshArgs splits a value into arguments as sshd does: on spaces outside
// quotes, with a backslash taking the next character as it is
// (`conf\ site.conf` is one path); before a glob character the
// backslash stays, for the glob to match that character itself.
func sshArgs(value string) []string {
	var out []string
	var arg strings.Builder
	var quote rune
	in, escaped := false, false
	for _, r := range value {
		switch {
		case escaped:
			// A glob character keeps its backslash, so Include matches it
			// as itself (site\[1].conf), as sshd's glob does.
			if strings.ContainsRune(`*?[]`, r) {
				arg.WriteRune('\\')
			}
			arg.WriteRune(r)
			escaped, in = false, true
		case r == '\\':
			escaped, in = true, true
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, in = r, true
		case quote == 0 && (r == ' ' || r == '\t'):
			if in {
				out = append(out, arg.String())
				arg.Reset()
				in = false
			}
		default:
			arg.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, arg.String())
	}
	return out
}

// firstWord is a value's first argument, without its quotes: what sshd
// reads for a yes/no or a number.
func firstWord(value string) string {
	value = strings.TrimSpace(value)
	for _, q := range []string{`"`, `'`} {
		if strings.HasPrefix(value, q) {
			if end := strings.Index(value[1:], q); end >= 0 {
				return value[1 : end+1]
			}
		}
	}
	if f := strings.Fields(value); len(f) > 0 {
		return f[0]
	}
	return ""
}

// get returns a keyword's value, or def with where it came from as
// OpenSSH's default.
func (s sshSettings) get(key, def string) sshValue {
	if v, ok := s.all[key]; ok {
		return v
	}
	if v, ok := s.values[key]; ok {
		return v
	}
	return sshValue{value: def}
}

// where says where a value came from, for a finding's detail.
func (v sshValue) where(keyword string) string {
	if v.file == "" {
		return fmt.Sprintf("%s is not set, and OpenSSH's default is %s", keyword, v.value)
	}
	return fmt.Sprintf("%s %s in %s", keyword, v.value, v.file)
}

// passwords reports whether sshd takes passwords, and why: password
// authentication, or keyboard-interactive through PAM (which asks for
// the same password).
//
// With AuthenticationMethods, a password counts only where some list asks
// for no key: `publickey,password` needs a key first.
func (s sshSettings) passwords() (bool, string) {
	if am := s.get("authenticationmethods", "any"); am.value != "any" {
		alone := false
		for _, list := range strings.Fields(am.value) {
			if !slices.Contains(strings.Split(list, ","), "publickey") {
				alone = true
			}
		}
		if !alone {
			return false, ""
		}
	}
	pw := s.get("passwordauthentication", "yes")
	if pw.value == "yes" {
		return true, pw.where("PasswordAuthentication")
	}
	kbd := s.get("kbdinteractiveauthentication", "yes")
	if kbd.value == "yes" && s.get("usepam", "no").value == "yes" {
		return true, kbd.where("KbdInteractiveAuthentication") + ", with UsePAM yes"
	}
	return false, ""
}

// wherePut says where a fix for key goes: in the file that sets it now
// (sshd keeps the first value read, so a later file can't override it),
// or at the top of /etc/ssh/sshd_config when nothing sets it.
func (s sshSettings) wherePut(key string) string {
	if v, ok := s.all[key]; ok {
		return "in " + v.file + " (under `Match all`)"
	}
	if v, ok := s.values[key]; ok {
		return "in " + v.file + ", where it is set now"
	}
	return "at the top of " + sshdConfig
}

// sshReadHelp is what to run when a config file can't be read.
const sshReadHelp = "run `sudo sshd -T | grep -Ei 'passwordauth|kbdinteractive|permitroot'` on the server to see its settings"

// checks are ssh_password and ssh_root.
func (s sshSettings) checks() []node.HostCheck {
	if !s.installed {
		return []node.HostCheck{
			skipped("ssh_password", "No SSH server is installed"),
			skipped("ssh_root", "No SSH server is installed"),
		}
	}
	pw, why := s.passwords()
	var out []node.HostCheck
	switch {
	case pw:
		out = append(out, finding("ssh_password", "", node.HostWarning, "SSH takes passwords",
			why+". Anyone on the internet can try to guess one.",
			"Set `PasswordAuthentication no` and `KbdInteractiveAuthentication no` "+s.wherePut("passwordauthentication")+
				", check it with `sudo sshd -t`, run `sudo systemctl reload ssh`, then sign in with your key in a new session before closing this one"))
	case s.unreadable != "":
		out = append(out, unknown("ssh_password", "Can't tell whether SSH takes passwords",
			"Dock can't read "+s.unreadable+", which may turn passwords on; "+sshReadHelp))
	default:
		out = append(out, pass("ssh_password", "SSH takes keys only"))
	}
	root := s.get("permitrootlogin", "prohibit-password")
	switch {
	case root.value == "yes" && pw:
		out = append(out, finding("ssh_root", "", node.HostCritical, "Root can sign in over SSH with a password",
			root.where("PermitRootLogin")+", and SSH takes passwords. A guessed root password is the whole server.",
			"Set `PermitRootLogin prohibit-password` and `PasswordAuthentication no` "+s.wherePut("permitrootlogin")+
				", check it with `sudo sshd -t`, then run `sudo systemctl reload ssh`"))
	case root.value == "yes" && s.unreadable != "":
		out = append(out, finding("ssh_root", "", node.HostWarning, "Root can sign in over SSH",
			root.where("PermitRootLogin")+". Dock can't read "+s.unreadable+", which may turn passwords on; "+sshReadHelp+".",
			"Set `PermitRootLogin prohibit-password` "+s.wherePut("permitrootlogin")+", check it with `sudo sshd -t`, then run `sudo systemctl reload ssh`"))
	case root.value == "yes":
		out = append(out, finding("ssh_root", "", node.HostInfo, "Root can sign in over SSH",
			root.where("PermitRootLogin")+". Only with a key, since SSH takes no passwords.",
			"Sign in as your own user and use sudo, then set `PermitRootLogin no` "+s.wherePut("permitrootlogin")+" and run `sudo sshd -t && sudo systemctl reload ssh`"))
	case s.unreadable != "":
		out = append(out, unknown("ssh_root", "Can't tell whether root can sign in over SSH",
			"Dock can't read "+s.unreadable+"; "+sshReadHelp))
	default:
		out = append(out, pass("ssh_root", "Root can't sign in over SSH with a password"))
	}
	return out
}

// isNotExist reports whether err is a missing file.
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
