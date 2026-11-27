package hostcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/NordTempo/tempoDock/internal/node"
)

// dockerTCP is docker_tcp: Docker's API listening on TCP, from
// /etc/docker/daemon.json's hosts, dockerd's command line, and a socket
// on Docker's API ports. A source it can't read makes it unknown, unless
// another already shows the API exposed.
func (c Checker) dockerTCP() node.HostCheck {
	var hosts, unread []string
	tls := false
	data, err := c.read("/etc/docker/daemon.json")
	if err != nil && !isNotExist(err) {
		unread = append(unread, "/etc/docker/daemon.json: "+err.Error())
	}
	if err == nil {
		var cfg struct {
			Hosts     []string `json:"hosts"`
			TLSVerify bool     `json:"tlsverify"`
		}
		if json.Unmarshal(data, &cfg) == nil {
			hosts, tls = append(hosts, cfg.Hosts...), cfg.TLSVerify
		}
	}
	args, err := c.dockerdArgs()
	if err != nil {
		unread = append(unread, "dockerd's command line: "+err.Error())
	}
	for i, a := range args {
		switch {
		case a == "-H" || a == "--host":
			if i+1 < len(args) {
				hosts = append(hosts, args[i+1])
			}
		case strings.HasPrefix(a, "--host="):
			hosts = append(hosts, strings.TrimPrefix(a, "--host="))
		case strings.HasPrefix(a, "-H") && len(a) > 2:
			hosts = append(hosts, strings.TrimPrefix(strings.TrimPrefix(a, "-H"), "="))
		case a == "--tlsverify" || a == "--tlsverify=true":
			tls = true
		}
	}
	var exposed []string
	for _, h := range hosts {
		if u, err := url.Parse(h); err == nil && u.Scheme == "tcp" && !loopbackHost(u.Hostname()) {
			exposed = append(exposed, h)
		}
	}
	ls, err := c.listeners()
	if err != nil {
		unread = append(unread, "the host's sockets: "+err.Error())
	}
	for _, l := range ls {
		if slices.Contains(dockerAPIPorts, l.port) && !l.addr.IsLoopback() {
			exposed = append(exposed, netip.AddrPortFrom(l.addr, uint16(l.port)).String())
		}
	}
	if len(exposed) == 0 && len(unread) > 0 {
		return unknown("docker_tcp", "Can't tell whether Docker's API is on TCP",
			"Dock can't read "+strings.Join(unread, "; ")+"; run `sudo ss -tlnp | grep dockerd` on the server to see")
	}
	if len(exposed) == 0 {
		return pass("docker_tcp", "Docker's API is not on TCP")
	}
	slices.Sort(exposed)
	exposed = slices.Compact(exposed)
	fix := "Remove the tcp:// host from /etc/docker/daemon.json or dockerd's -H flags (`systemctl cat docker`), then `sudo systemctl restart docker`. Dock reaches Docker through its socket"
	if tls {
		return finding("docker_tcp", "", node.HostWarning, "Docker's API is on TCP, with client certificates",
			"Listening on "+strings.Join(exposed, ", ")+". Anyone holding a client certificate controls every container and the host.", fix)
	}
	return finding("docker_tcp", "", node.HostCritical, "Docker's API is open on TCP without TLS",
		"Listening on "+strings.Join(exposed, ", ")+". Whoever reaches it runs anything as root on this server.", fix)
}

// loopbackHost reports whether a tcp:// host only takes local connections.
func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.IsLoopback()
}

// dockerdArgs returns dockerd's command line, from the first /proc entry
// named dockerd; none without one. A /proc or a command line it can't
// read is an error: dockerd's flags may open the API on any port.
func (c Checker) dockerdArgs() ([]string, error) {
	entries, err := os.ReadDir(c.path("/proc"))
	if isNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if name := e.Name(); name[0] < '0' || name[0] > '9' {
			continue
		}
		comm, err := c.read(filepath.Join("/proc", e.Name(), "comm"))
		switch {
		case isNotExist(err) || errors.Is(err, syscall.ESRCH):
			continue // exited while listing
		case err != nil:
			// /proc mounted with hidepid: dockerd can't be told apart.
			return nil, err
		case string(bytes.TrimSpace(comm)) != "dockerd":
			continue
		}
		cmdline, err := c.read(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			return nil, err
		}
		return strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00"), nil
	}
	return nil, nil
}

// maxSocketFindings caps docker_socket's findings, as maxPortFindings
// caps open_port's: together they keep a host's results well under what
// the link accepts (remote's maxHostChecks).
const maxSocketFindings = 50

// socketPaths are host paths whose mount hands a container the Docker
// socket.
var socketPaths = []string{"/var/run/docker.sock", "/run/docker.sock", "/var/run", "/run", "/"}

// dockerSocket is docker_socket: a finding per running container that
// has the Docker socket mounted.
func dockerSocket(containers []Container, err error) []node.HostCheck {
	if err != nil {
		return []node.HostCheck{unknown("docker_socket", "Can't tell which containers hold the Docker socket",
			"Dock can't list the host's containers: "+err.Error())}
	}
	var out []node.HostCheck
	for _, ct := range containers {
		if len(out) == maxSocketFindings {
			break
		}
		i := slices.IndexFunc(ct.Mounts, func(m string) bool { return slices.Contains(socketPaths, filepath.Clean(m)) })
		if i < 0 {
			continue
		}
		whose := "Dock didn't start it."
		if ct.Dock {
			whose = "It is one of Dock's services."
		}
		out = append(out, finding("docker_socket", ct.Name, node.HostWarning, "Container "+ct.Name+" holds the Docker socket",
			"It mounts "+ct.Mounts[i]+", so whoever takes over the container controls every container and the host as root. "+whose,
			"Remove the mount if the container doesn't need Docker, or the container if nothing needs it: `docker rm -f "+ct.Name+"`"))
	}
	if len(out) == 0 {
		return []node.HostCheck{pass("docker_socket", "No container holds the Docker socket")}
	}
	if len(out) == maxSocketFindings {
		out[len(out)-1].Detail += " Dock lists the first " + strconv.Itoa(maxSocketFindings) + "; `docker ps` shows the rest."
	}
	return out
}
