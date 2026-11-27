package hostcheck

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	// aptTimeout bounds the simulated upgrade: it reads the package lists
	// and dpkg's status, a second or two.
	aptTimeout = time.Minute
	// staleLists is when package lists are old enough to say the count
	// may miss newer updates.
	staleLists = 7 * 24 * time.Hour
	// rebootWarnAfter is how long a waiting reboot is only info.
	rebootWarnAfter = 7 * 24 * time.Hour
)

// packages are the apt checks: security_updates, reboot_required and
// auto_updates. A host without apt skips them.
func (c Checker) packages(ctx context.Context) []node.HostCheck {
	if !c.exists("/usr/bin/apt-get") {
		const why = "Not an apt system; Dock checks updates on Debian and Ubuntu only"
		return []node.HostCheck{skipped("security_updates", why), skipped("reboot_required", why), skipped("auto_updates", why)}
	}
	return []node.HostCheck{c.securityUpdates(ctx), c.rebootRequired(), c.autoUpdates(ctx)}
}

// runApt simulates a full upgrade from the local package lists: no
// download, no lock, no change.
func runApt(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, aptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "apt-get", "--simulate", "--quiet", "-o", "Debug::NoLocking=true", "dist-upgrade")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive")
	return cmd.Output()
}

// securityUpdates is security_updates: the upgrades a simulated
// dist-upgrade would install from a security archive.
func (c Checker) securityUpdates(ctx context.Context) node.HostCheck {
	apt := c.Apt
	if apt == nil {
		apt = runApt
	}
	out, err := apt(ctx)
	if err != nil {
		msg := err.Error()
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return unknown("security_updates", "Can't tell which updates are waiting", "`apt-get --simulate dist-upgrade` failed: "+clip(msg, 300))
	}
	security, others := aptUpgrades(out)
	stale := ""
	if age, ok := c.listsAge(); ok && age > staleLists {
		stale = fmt.Sprintf(" Package lists were last refreshed %d days ago, so newer updates may be missing: is apt-daily.timer on?", int(age.Hours()/24))
	}
	if len(security) == 0 {
		p := pass("security_updates", "No security updates are waiting")
		if others > 0 {
			p.Detail = fmt.Sprintf("%d other updates are.", others)
		}
		p.Detail = strings.TrimSpace(p.Detail + stale)
		return p
	}
	names := strings.Join(security[:min(len(security), 3)], ", ")
	if n := len(security) - 3; n > 0 {
		names += fmt.Sprintf(" and %d more", n)
	}
	title := fmt.Sprintf("%d security updates are waiting", len(security))
	if len(security) == 1 {
		title = "1 security update is waiting"
	}
	return finding("security_updates", "", node.HostWarning, title, names+"."+stale,
		"Run `sudo apt-get update && sudo apt-get upgrade` on the server; turning on unattended-upgrades keeps them coming")
}

// aptUpgrades reads apt-get --simulate's output: the packages it would
// install from a security archive, sorted, and how many others.
func aptUpgrades(out []byte) (security []string, others int) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// Inst openssl [3.0.11-1] (3.0.15-1 Debian-Security:12/stable-security [amd64])
		origin := line[strings.LastIndexByte(line, '(')+1:]
		if strings.Contains(origin, "-security") || strings.Contains(origin, "Debian-Security") {
			security = append(security, f[1])
		} else {
			others++
		}
	}
	slices.Sort(security)
	return slices.Compact(security), others
}

// listsAge is how long ago apt last refreshed its package lists.
func (c Checker) listsAge() (time.Duration, bool) {
	for _, p := range []string{"/var/lib/apt/periodic/update-success-stamp", "/var/lib/apt/lists"} {
		if fi, err := c.stat(p); err == nil {
			return c.now().Sub(fi.ModTime()), true
		}
	}
	return 0, false
}

// rebootRequired is reboot_required: the marker apt's hooks leave when
// an update (a kernel, libc) needs a restart.
func (c Checker) rebootRequired() node.HostCheck {
	fi, err := c.stat("/run/reboot-required")
	if err != nil {
		fi, err = c.stat("/var/run/reboot-required")
	}
	if err != nil {
		return pass("reboot_required", "No reboot is waiting")
	}
	age := c.now().Sub(fi.ModTime())
	detail := "The fixes it brings don't apply until the server restarts."
	if pkgs := c.rebootPackages(); len(pkgs) > 0 {
		detail = "For " + strings.Join(pkgs[:min(len(pkgs), 3)], ", ") + ". " + detail
	}
	fix := "Restart the server when it suits: `sudo reboot`. Drain it from its console page first to keep its apps up"
	if age < rebootWarnAfter {
		return finding("reboot_required", "", node.HostInfo, "A reboot is waiting", detail, fix)
	}
	return finding("reboot_required", "", node.HostWarning, fmt.Sprintf("A reboot has waited %d days", int(age.Hours()/24)), detail, fix)
}

// rebootPackages are the packages that asked for the reboot.
func (c Checker) rebootPackages() []string {
	data, err := c.read("/run/reboot-required.pkgs")
	if err != nil {
		data, _ = c.read("/var/run/reboot-required.pkgs")
	}
	pkgs := strings.Fields(string(data))
	slices.Sort(pkgs)
	return slices.Compact(pkgs)
}

// autoUpdates is auto_updates: unattended-upgrades installed and turned
// on (APT::Periodic::Unattended-Upgrade), as APT's own parser reads it
// with apt-config: its files, their order, scopes and apt.conf.
func (c Checker) autoUpdates(ctx context.Context) node.HostCheck {
	const fix = "Run `sudo apt-get install unattended-upgrades && sudo dpkg-reconfigure -plow unattended-upgrades`"
	if !c.exists("/usr/bin/unattended-upgrade") {
		return finding("auto_updates", "", node.HostInfo, "Security updates don't install by themselves",
			"unattended-upgrades isn't installed.", fix)
	}
	read := c.AptConfig
	if read == nil {
		read = aptConfig
	}
	v, err := read(ctx, "APT::Periodic::Unattended-Upgrade")
	if err != nil {
		return unknown("auto_updates", "Can't tell whether security updates install by themselves", "`apt-config` failed: "+clip(err.Error(), 300))
	}
	if v == "" || v == "0" || strings.EqualFold(v, "false") {
		return finding("auto_updates", "", node.HostInfo, "Security updates don't install by themselves",
			"unattended-upgrades is installed but APT::Periodic::Unattended-Upgrade isn't on.", fix)
	}
	return pass("auto_updates", "Security updates install by themselves")
}

// aptConfig reads one setting with APT's parser: `apt-config shell`
// prints V='value', or nothing when it isn't set.
func aptConfig(ctx context.Context, key string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "apt-config", "shell", "V", key).Output()
	if err != nil {
		return "", err
	}
	v, _ := strings.CutPrefix(strings.TrimSpace(string(out)), "V=")
	return strings.Trim(v, "'"), nil
}

// clip shortens s to at most n bytes.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
