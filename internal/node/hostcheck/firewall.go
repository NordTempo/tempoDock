package hostcheck

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/node"
)

// hostFirewall is host_firewall: an inbound firewall on the host, from
// what an unprivileged user can read: ufw turned on, firewalld enabled,
// or nftables enabled with a ruleset that drops or rejects something.
// Listing the rules in force needs root, and a provider's firewall in
// front of the server doesn't show here at all, so a miss is only info.
// Its fix opens the ports sshd listens on before turning ufw on, so it
// doesn't lock the operator out; where Dock couldn't read all of sshd's
// config, it asks the operator to find them first.
func (c Checker) hostFirewall(ssh sshSettings) node.HostCheck {
	if data, err := c.read("/etc/ufw/ufw.conf"); err == nil && confValue(data, "ENABLED") == "yes" {
		return pass("host_firewall", "ufw is on")
	}
	if c.exists("/etc/systemd/system/multi-user.target.wants/firewalld.service") {
		return pass("host_firewall", "firewalld is on")
	}
	if c.exists("/etc/systemd/system/sysinit.target.wants/nftables.service") {
		if data, err := c.read("/etc/nftables.conf"); err == nil && dropsSomething(data) {
			return pass("host_firewall", "nftables is on")
		}
	}
	return finding("host_firewall", "", node.HostInfo, "No host firewall found",
		"Neither ufw, firewalld nor an nftables ruleset that drops anything is on. A provider's firewall in front of the server doesn't show from here: "+
			"dismiss this if one keeps everything but SSH, HTTP and HTTPS out. Docker's published ports go past ufw and firewalld either way.",
		ufwFix(ssh))
}

// ufwFix is host_firewall's fix: a provider's firewall, or ufw with SSH's
// ports, HTTP and HTTPS allowed.
func ufwFix(ssh sshSettings) string {
	if ssh.installed && ssh.unreadable != "" {
		return "Turn on your provider's firewall, or find SSH's ports in `sudo sshd -T | grep -E '^(port|listenaddress) '` and `sudo ss -tlnp | grep -E 'sshd|systemd'` " +
			"(`systemctl status ssh.socket` where sshd starts from a socket), allow each with `sudo ufw allow <port>/tcp`, then run `sudo ufw allow 80,443/tcp && sudo ufw enable`"
	}
	return "Turn on your provider's firewall, or check that `sudo ss -tlnp | grep -E 'sshd|systemd'` (and `systemctl status ssh.socket`, where sshd starts from a socket) shows SSH on these ports and run `" + ufwCommand(ssh.ports) + "`"
}

// ufwCommand turns ufw on with SSH's ports, HTTP and HTTPS allowed.
func ufwCommand(sshPorts []int) string {
	var b strings.Builder
	for _, p := range sshPorts {
		fmt.Fprintf(&b, "sudo ufw allow %d/tcp && ", p)
	}
	return b.String() + "sudo ufw allow 80,443/tcp && sudo ufw enable"
}

// confValue reads KEY=value from a shell-style config file.
func confValue(data []byte, key string) string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), key+"="); ok {
			return strings.ToLower(strings.Trim(v, `"'`))
		}
	}
	return ""
}

// dropsSomething reports whether an nftables ruleset has a drop or reject
// outside comments.
func dropsSomething(data []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		for _, w := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == ';' || r == ',' }) {
			if w == "drop" || w == "reject" {
				return true
			}
		}
	}
	return false
}

// appFirewall is app_firewall: Dock's own firewall, which keeps apps and
// builds off the server's private networks (internal/node/firewall). Its
// own platform alert (core's firewall alert) reports a miss as well.
func appFirewall(applied func() (bool, error)) node.HostCheck {
	ok, err := applied()
	switch {
	case err != nil:
		return unknown("app_firewall", "Can't tell whether Dock's app firewall is installed", err.Error())
	case ok:
		return pass("app_firewall", "Dock's app firewall is installed")
	}
	return finding("app_firewall", "", node.HostWarning, "Dock's app firewall is not installed",
		"Apps and builds on this server can reach its private networks and the server itself.",
		"Run `sudo systemctl restart dock-firewall` and check `systemctl status dock-firewall`, then restart Dock on the server")
}
