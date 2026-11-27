package hostcheck

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// host lays out files under a temp root, like a server's filesystem.
func host(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// byKey runs the checker and indexes its results by finding key.
func byKey(t *testing.T, c Checker) map[string]node.HostCheck {
	t.Helper()
	if c.Apt == nil {
		c.Apt = func(context.Context) ([]byte, error) { return nil, nil }
	}
	res, err := c.CheckHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]node.HostCheck{}
	for _, r := range res {
		if _, dup := out[r.Key()]; dup {
			t.Fatalf("two results for %s", r.Key())
		}
		out[r.Key()] = r
	}
	return out
}

// want checks one result's status and severity.
func want(t *testing.T, got map[string]node.HostCheck, key string, status node.HostCheckStatus, severity string) node.HostCheck {
	t.Helper()
	r, ok := got[key]
	if !ok {
		t.Fatalf("no result for %s; have %v", key, keys(got))
	}
	if r.Status != status || r.Severity != severity {
		t.Fatalf("%s: got %s/%s (%s: %s), want %s/%s", key, r.Status, r.Severity, r.Title, r.Detail, status, severity)
	}
	return r
}

func keys(m map[string]node.HostCheck) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSSH(t *testing.T) {
	t.Run("cloud-init turns passwords and root on", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config":                         "Include /etc/ssh/sshd_config.d/*.conf\nPasswordAuthentication no\n",
			"/etc/ssh/sshd_config.d/50-cloud-init.conf":    "PasswordAuthentication yes\nPermitRootLogin yes\n",
			"/etc/ssh/sshd_config.d/60-match-ignored.conf": "Match User deploy\n  PasswordAuthentication no\n",
		})})
		r := want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
		if !strings.Contains(r.Detail, "50-cloud-init.conf") {
			t.Errorf("detail doesn't say where: %s", r.Detail)
		}
		want(t, got, "ssh_root", node.HostFinding, node.HostCritical)
	})
	t.Run("the first value read wins", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config":                      "Include sshd_config.d/*.conf\nKbdInteractiveAuthentication no\nUsePAM yes\n",
			"/etc/ssh/sshd_config.d/00-dock.conf":       "PasswordAuthentication no\nPermitRootLogin=yes\n",
			"/etc/ssh/sshd_config.d/50-cloud-init.conf": "PasswordAuthentication yes\n",
		})})
		want(t, got, "ssh_password", node.HostPass, "")
		want(t, got, "ssh_root", node.HostFinding, node.HostInfo)
	})
	t.Run("defaults take passwords", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{"/etc/ssh/sshd_config": "# nothing set\n"})})
		r := want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
		if !strings.Contains(r.Detail, "default") {
			t.Errorf("detail doesn't say it's the default: %s", r.Detail)
		}
		want(t, got, "ssh_root", node.HostPass, "")
	})
	t.Run("an unreadable include can't tell", func(t *testing.T) {
		root := host(t, map[string]string{
			"/etc/ssh/sshd_config":                      "Include /etc/ssh/sshd_config.d/*.conf\nPasswordAuthentication no\n",
			"/etc/ssh/sshd_config.d/50-cloud-init.conf": "PasswordAuthentication yes\n",
		})
		if err := os.Chmod(filepath.Join(root, "/etc/ssh/sshd_config.d/50-cloud-init.conf"), 0); err != nil {
			t.Fatal(err)
		}
		if os.Geteuid() == 0 {
			t.Skip("root reads every file")
		}
		got := byKey(t, Checker{Root: root})
		want(t, got, "ssh_password", node.HostUnknown, "")
	})
	t.Run("Match all overrides the global section, trailing comments don't count", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config": "PasswordAuthentication no # keys only\nPermitRootLogin no\nMatch all\n  PermitRootLogin yes # for now\n",
		})})
		want(t, got, "ssh_password", node.HostPass, "")
		want(t, got, "ssh_root", node.HostFinding, node.HostInfo)
	})
	t.Run("an unreadable include directory can't tell", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every directory")
		}
		root := host(t, map[string]string{
			"/etc/ssh/sshd_config":                      "Include /etc/ssh/sshd_config.d/*.conf\nPasswordAuthentication no\n",
			"/etc/ssh/sshd_config.d/50-cloud-init.conf": "PasswordAuthentication yes\n",
		})
		dir := filepath.Join(root, "/etc/ssh/sshd_config.d")
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		got := byKey(t, Checker{Root: root})
		want(t, got, "ssh_password", node.HostUnknown, "")
	})
	t.Run("an Include under Match all is read under it", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config": "PasswordAuthentication no\nKbdInteractiveAuthentication no\nMatch all\n  Include /etc/ssh/allow.conf\n",
			"/etc/ssh/allow.conf":  "PasswordAuthentication yes\n",
		})})
		want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
	})
	t.Run("ListenAddress with a port is SSH's port", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config": "ListenAddress 0.0.0.0:2222\nListenAddress [::]:2222\nPasswordAuthentication no\n",
			"/proc/net/tcp":        procTCP("00000000:08AE"), // 0.0.0.0:2222
		})})
		want(t, got, "open_port", node.HostPass, "")
		if r := got["host_firewall"]; !strings.Contains(r.Fix, "ufw allow 2222/tcp") || strings.Contains(r.Fix, "allow 22/tcp") {
			t.Errorf("host_firewall fix: %s", r.Fix)
		}
	})
	t.Run("a bare ListenAddress keeps the default port", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config": "ListenAddress 203.0.113.5\nListenAddress 0.0.0.0:2222\nPasswordAuthentication no\n",
		})})
		if r := got["host_firewall"]; !strings.Contains(r.Fix, "ufw allow 22/tcp && sudo ufw allow 2222/tcp") {
			t.Errorf("host_firewall fix: %s", r.Fix)
		}
	})
	t.Run("root with passwords maybe on is a warning", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every file")
		}
		root := host(t, map[string]string{
			"/etc/ssh/sshd_config": "Include /etc/ssh/hidden.conf\nPermitRootLogin yes\nPasswordAuthentication no\n",
			"/etc/ssh/hidden.conf": "PasswordAuthentication yes\n",
		})
		if err := os.Chmod(filepath.Join(root, "/etc/ssh/hidden.conf"), 0); err != nil {
			t.Fatal(err)
		}
		got := byKey(t, Checker{Root: root})
		r := want(t, got, "ssh_root", node.HostFinding, node.HostWarning)
		if strings.Contains(r.Detail, "takes no passwords") || !strings.Contains(r.Detail, "hidden.conf") {
			t.Errorf("ssh_root detail: %s", r.Detail)
		}
	})
	t.Run("an escaped space is part of an Include path", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config":    "Include /etc/ssh/conf\\ site.conf\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n",
			"/etc/ssh/conf site.conf": "PasswordAuthentication yes\n",
		})})
		want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
	})
	t.Run("a file where a wildcard reads directories matches nothing", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config":      "Include /etc/ssh/conf.d/*/*.conf\nPasswordAuthentication no\n",
			"/etc/ssh/conf.d/README":    "not a directory\n",
			"/etc/ssh/conf.d/b/ok.conf": "PermitRootLogin no\n",
		})})
		want(t, got, "ssh_password", node.HostPass, "")
	})
	t.Run("an escaped quote doesn't end a quoted path", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config":       "Include \"/etc/ssh/conf\\\" #site.conf\"\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n",
			"/etc/ssh/conf\" #site.conf": "PasswordAuthentication yes\n",
		})})
		want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
	})
	t.Run("an escaped glob character matches itself", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config":  "Include /etc/ssh/site\\[1].conf\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n",
			"/etc/ssh/site[1].conf": "PasswordAuthentication yes\n",
			"/etc/ssh/site1.conf":   "PasswordAuthentication no\n",
		})})
		want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
	})
	t.Run("a double backslash is one, and escapes the glob character after it", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config":  "Include /etc/ssh/site\\\\[1].conf\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n",
			"/etc/ssh/site[1].conf": "PasswordAuthentication yes\n",
		})})
		want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
	})
	t.Run("single quotes are quotes", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config": "PasswordAuthentication 'yes' # temporary\nKbdInteractiveAuthentication no\n",
		})})
		want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
	})
	t.Run("an unreadable directory before a literal element can't tell", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every directory")
		}
		root := host(t, map[string]string{
			"/etc/ssh/sshd_config":                   "Include /etc/ssh/conf.d/*/nested/*.conf\nPasswordAuthentication no\n",
			"/etc/ssh/conf.d/a/nested/50-cloud.conf": "PasswordAuthentication yes\n",
			"/etc/ssh/conf.d/b/nested/ok.conf":       "PermitRootLogin no\n",
			"/etc/ssh/conf.d/c":                      "a file, not a directory\n",
		})
		dir := filepath.Join(root, "/etc/ssh/conf.d/a")
		if err := os.Chmod(dir, 0o600); err != nil { // readable list, no entering
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		got := byKey(t, Checker{Root: root})
		want(t, got, "ssh_password", node.HostUnknown, "")
	})
	t.Run("a # inside a word isn't a comment", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/ssh/sshd_config":                "Include /etc/ssh/sshd_config.d/10#site.conf\nPasswordAuthentication no\n",
			"/etc/ssh/sshd_config.d/10#site.conf": "PasswordAuthentication yes\n",
		})})
		want(t, got, "ssh_password", node.HostFinding, node.HostWarning)
	})
	t.Run("an unreadable directory a wildcard reaches can't tell", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every directory")
		}
		root := host(t, map[string]string{
			"/etc/ssh/sshd_config":            "Include /etc/ssh/conf.d/*/*.conf\nPasswordAuthentication no\n",
			"/etc/ssh/conf.d/a/50-cloud.conf": "PasswordAuthentication yes\n",
			"/etc/ssh/conf.d/b/ok.conf":       "PermitRootLogin no\n",
		})
		dir := filepath.Join(root, "/etc/ssh/conf.d/a")
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		got := byKey(t, Checker{Root: root})
		want(t, got, "ssh_password", node.HostUnknown, "")
		// SSH's port can't be known for sure: ufw's fix says to find it.
		if r := got["host_firewall"]; !strings.Contains(r.Fix, "listenaddress") || strings.Contains(r.Fix, "allow 22/tcp") {
			t.Errorf("host_firewall fix: %s", r.Fix)
		}
	})
	t.Run("no SSH server", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, nil)})
		want(t, got, "ssh_password", node.HostSkipped, "")
	})
}

// procTCP is a /proc/net/tcp table of LISTEN sockets.
func procTCP(addrs ...string) string {
	var b strings.Builder
	b.WriteString("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n")
	for i, a := range addrs {
		b.WriteString("   " + string(rune('0'+i)) + ": " + a + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0\n")
	}
	return b.String()
}

func TestOpenPorts(t *testing.T) {
	root := host(t, map[string]string{
		"/etc/ssh/sshd_config": "Port 2222\nPasswordAuthentication no\n",
		"/proc/net/tcp": procTCP(
			"00000000:08AE", // 0.0.0.0:2222, sshd
			"00000000:0050", // 0.0.0.0:80, Caddy
			"0100007F:1538", // 127.0.0.1:5432
			"00000000:1F90", // 0.0.0.0:8080
			"02014064:2328", // 100.64.1.2:9000, Tailscale
			"0100A8C0:2329", // 192.168.0.1:9001, LAN
			"00000000:1539", // 0.0.0.0:5433, a container's
			"00000000:092F", // 0.0.0.0:2351, docker_tcp's
			"0100007F:3C50", // 127.0.0.1:15440, a Dock container's
			"057100CB:3C50", // 203.0.113.5:15440, something else's
			"067100CB:3C51", // 203.0.113.6:15441, the tools container's
			"057100CB:3C51", // 203.0.113.5:15441, something else's
		),
		"/proc/net/tcp6": procTCP(
			"00000000000000000000000000000000:1F90", // [::]:8080
			"5C117AFD0000E0A100000000413A2D36:A3DE", // [fd7a:115c:a1e0::362d:3a41]:41950, Tailscale
		),
	})
	c := Checker{Root: root, DockPorts: []int{80, 443}, Containers: func(context.Context) ([]Container, error) {
		return []Container{
			{Name: "pg-test", Ports: []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:5433"), netip.MustParseAddrPort("[::]:5433")}},
			{Name: "web", Dock: true, Ports: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:15440")}},
			{Name: "tools", Ports: []netip.AddrPort{netip.MustParseAddrPort("203.0.113.6:15441")}},
			// Published with Docker's userland proxy off: no listener.
			{Name: "mongo", Ports: []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:27017")}},
		}, nil
	}}
	got := byKey(t, c)
	r := want(t, got, "open_port:8080", node.HostFinding, node.HostWarning)
	if !strings.Contains(r.Title, "every address") {
		t.Errorf("title: %s", r.Title)
	}
	r = want(t, got, "open_port:5433", node.HostFinding, node.HostWarning)
	if !strings.Contains(r.Detail, "pg-test") {
		t.Errorf("detail doesn't name the container: %s", r.Detail)
	}
	want(t, got, "open_port:2351", node.HostFinding, node.HostWarning)
	// A Dock container on loopback says nothing of a public listener on
	// the same port.
	r = want(t, got, "open_port:15440", node.HostFinding, node.HostWarning)
	if !strings.Contains(r.Title, "203.0.113.5") || strings.Contains(r.Detail, "web") {
		t.Errorf("15440: %q %q", r.Title, r.Detail)
	}
	// Two listeners on one port, one of them a container's: the finding
	// doesn't tell anyone to remove that container for both.
	r = want(t, got, "open_port:15441", node.HostFinding, node.HostWarning)
	if strings.Contains(r.Detail, "tools") || strings.Contains(r.Fix, "docker rm") {
		t.Errorf("15441: %q %q", r.Detail, r.Fix)
	}
	r = want(t, got, "open_port:27017", node.HostFinding, node.HostWarning)
	if !strings.Contains(r.Detail, "mongo") {
		t.Errorf("27017: %q", r.Detail)
	}
	// ufw's fix opens the SSH port sshd really uses.
	if r := got["host_firewall"]; !strings.Contains(r.Fix, "ufw allow 2222/tcp") {
		t.Errorf("host_firewall fix: %s", r.Fix)
	}
	// A private address is info: reachable from the internet only where
	// the provider maps a public address to it.
	r = want(t, got, "open_port:9001", node.HostFinding, node.HostInfo)
	if !strings.Contains(r.Title, "private address 192.168.0.1") {
		t.Errorf("9001: %q", r.Title)
	}
	for _, k := range []string{"open_port:2222", "open_port:80", "open_port:5432", "open_port:9000", "open_port:41950", "open_port"} {
		if _, ok := got[k]; ok {
			t.Errorf("unexpected %s", k)
		}
	}
}

func TestDocker(t *testing.T) {
	t.Run("API on TCP without TLS", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/docker/daemon.json": `{"hosts": ["unix:///var/run/docker.sock", "tcp://0.0.0.0:2375"]}`,
		})})
		want(t, got, "docker_tcp", node.HostFinding, node.HostCritical)
	})
	t.Run("dockerd's flags, with TLS", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/proc/812/comm":    "dockerd\n",
			"/proc/812/cmdline": "/usr/bin/dockerd\x00-H\x00fd://\x00--host=tcp://10.0.0.5:2376\x00--tlsverify\x00",
		})})
		want(t, got, "docker_tcp", node.HostFinding, node.HostWarning)
	})
	t.Run("dockerd's flags unreadable can't tell", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every file")
		}
		root := host(t, map[string]string{
			"/proc/812/comm":    "dockerd\n",
			"/proc/812/cmdline": "/usr/bin/dockerd\x00-H\x00tcp://0.0.0.0:4243\x00",
			"/proc/net/tcp":     procTCP(),
		})
		if err := os.Chmod(filepath.Join(root, "/proc/812/cmdline"), 0); err != nil {
			t.Fatal(err)
		}
		got := byKey(t, Checker{Root: root})
		want(t, got, "docker_tcp", node.HostUnknown, "")
	})
	t.Run("process names hidden can't tell", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every file")
		}
		root := host(t, map[string]string{
			"/proc/812/comm": "dockerd\n",
			"/proc/net/tcp":  procTCP(),
		})
		if err := os.Chmod(filepath.Join(root, "/proc/812/comm"), 0); err != nil {
			t.Fatal(err)
		}
		got := byKey(t, Checker{Root: root})
		want(t, got, "docker_tcp", node.HostUnknown, "")
	})
	t.Run("loopback only", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, map[string]string{
			"/etc/docker/daemon.json": `{"hosts": ["tcp://127.0.0.1:2375"]}`,
			"/proc/net/tcp":           procTCP("0100007F:0947"),
		})})
		want(t, got, "docker_tcp", node.HostPass, "")
	})
	t.Run("no sockets to read can't tell", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, nil)})
		want(t, got, "docker_tcp", node.HostUnknown, "")
	})
	t.Run("socket mounts", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, nil), Containers: func(context.Context) ([]Container, error) {
			return []Container{
				{Name: "portainer", Mounts: []string{"/var/run/docker.sock"}},
				{Name: "web", Dock: true, Mounts: []string{"/var/lib/dock/probe"}},
			}, nil
		}})
		want(t, got, "docker_socket:portainer", node.HostFinding, node.HostWarning)
		if _, ok := got["docker_socket:web"]; ok {
			t.Error("web doesn't hold the socket")
		}
	})
	t.Run("socket findings are capped", func(t *testing.T) {
		var many []Container
		for i := range maxSocketFindings + 10 {
			many = append(many, Container{Name: fmt.Sprintf("c%d", i), Mounts: []string{"/run/docker.sock"}})
		}
		res, err := Checker{Root: host(t, nil), Apt: func(context.Context) ([]byte, error) { return nil, nil },
			Containers: func(context.Context) ([]Container, error) { return many, nil }}.CheckHost(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range res {
			if r.Check == "docker_socket" {
				n++
			}
		}
		if n != maxSocketFindings {
			t.Fatalf("%d docker_socket results, want %d", n, maxSocketFindings)
		}
	})
	t.Run("no container list", func(t *testing.T) {
		got := byKey(t, Checker{Root: host(t, nil), Containers: func(context.Context) ([]Container, error) {
			return nil, errors.New("permission denied")
		}})
		want(t, got, "docker_socket", node.HostUnknown, "")
	})
}

func TestPackages(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	root := host(t, map[string]string{
		"/usr/bin/apt-get":                           "",
		"/run/reboot-required":                       "*** System restart required ***\n",
		"/run/reboot-required.pkgs":                  "linux-image-6.12.94-amd64\nlinux-base\n",
		"/usr/bin/unattended-upgrade":                "",
		"/var/lib/apt/periodic/update-success-stamp": "",
	})
	if err := os.Chtimes(filepath.Join(root, "/run/reboot-required"), now, now.Add(-9*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "/var/lib/apt/periodic/update-success-stamp"), now, now.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	apt := []byte(`Reading package lists...
Inst libssl3 [3.0.11-1~deb12u2] (3.0.15-1~deb12u1 Debian-Security:12/stable-security [amd64])
Inst openssl [3.0.11-1~deb12u2] (3.0.15-1~deb12u1 Debian-Security:12/stable-security [amd64])
Inst curl [7.88.1-10] (7.88.1-10+deb12u8 Debian:12.8/stable [amd64])
Inst libc6 [2.39-0ubuntu8] (2.39-0ubuntu8.3 Ubuntu:24.04/noble-security [amd64])
Conf libssl3 (3.0.15-1~deb12u1 Debian-Security:12/stable-security [amd64])
`)
	// APT's parser says what wins; the fragments above don't decide it.
	aptConfig := func(_ context.Context, key string) (string, error) {
		if key != "APT::Periodic::Unattended-Upgrade" {
			t.Errorf("asked apt-config for %s", key)
		}
		return "0", nil
	}
	got := byKey(t, Checker{Root: root, Now: func() time.Time { return now }, AptConfig: aptConfig,
		Apt: func(context.Context) ([]byte, error) { return apt, nil }})
	r := want(t, got, "security_updates", node.HostFinding, node.HostWarning)
	if r.Title != "3 security updates are waiting" || !strings.HasPrefix(r.Detail, "libc6, libssl3, openssl.") || !strings.Contains(r.Detail, "10 days ago") {
		t.Errorf("security_updates: %q %q", r.Title, r.Detail)
	}
	r = want(t, got, "reboot_required", node.HostFinding, node.HostWarning)
	if r.Title != "A reboot has waited 9 days" || !strings.Contains(r.Detail, "linux-image-6.12.94-amd64") {
		t.Errorf("reboot_required: %q %q", r.Title, r.Detail)
	}
	want(t, got, "auto_updates", node.HostFinding, node.HostInfo)

	got = byKey(t, Checker{Root: host(t, nil)})
	want(t, got, "security_updates", node.HostSkipped, "")
}

func TestFirewalls(t *testing.T) {
	got := byKey(t, Checker{Root: host(t, map[string]string{"/etc/ufw/ufw.conf": "# comment\nENABLED=yes\n"}),
		FirewallApplied: func() (bool, error) { return false, nil }})
	want(t, got, "host_firewall", node.HostPass, "")
	want(t, got, "app_firewall", node.HostFinding, node.HostWarning)

	got = byKey(t, Checker{Root: host(t, map[string]string{
		"/etc/systemd/system/sysinit.target.wants/nftables.service": "",
		"/etc/nftables.conf": "table inet filter {\n chain input { type filter hook input priority 0; } # drop nothing\n}\n",
	})})
	want(t, got, "host_firewall", node.HostFinding, node.HostInfo)
	if _, ok := got["app_firewall"]; ok {
		t.Error("app_firewall without FirewallApplied")
	}
}
