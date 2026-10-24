package stats

import (
	"bytes"
	"cmp"
	"context"
	"slices"

	"github.com/NordTempo/tempoDock/internal/node"
)

// The memory breakdown (node.HostStats.MemoryParts) says where a server's
// used memory goes. It walks the cgroup v2 tree one level below the root
// and system.slice: every systemd unit, slice and container is a cgroup
// whose memory.stat says what its processes hold. A process's part is anon
// plus shmem, the memory that can't be reclaimed: MemAvailable counts page
// cache as available, so counting it here would explain memory that isn't
// "used". Kernel memory comes from /proc/meminfo. What is left is mostly
// the free memory the kernel holds back (MemAvailable subtracts its
// watermarks) and is reported as "reserves and other", so the parts add up
// to the used total.
//
// A sample reads one memory.stat per unit and container (a few dozen
// small files) and lists two directories. Containers are described once
// through Options.Container and remembered while they exist.

// maxSystemParts is how many MemorySystem parts are reported by name; the
// rest are summed into one, so a busy server doesn't send a long list.
const maxSystemParts = 10

// platformUnits names the systemd units that are Dock's infrastructure.
// Units with the same name are summed.
var platformUnits = map[string]string{
	"dock.service":             "dock",
	"dock-node.service":        "dock",
	"dock-update.service":      "dock",
	"dock-firewall.service":    "dock",
	"dock-caddy.service":       "Caddy",
	"dock-cloudflared.service": "cloudflared",
	"tempobase.service":        "tempoBase",
	"docker.service":           "Docker engine",
	"docker.socket":            "Docker engine",
	"containerd.service":       "Docker engine",
}

// owner is a remembered cgroup: who it is, and the walk that last saw it.
// The caches hold pointers so a sample marks what it saw without
// allocating map keys.
type owner struct {
	part node.MemoryPart
	gen  uint32
}

// kernelMemory is the kernel's own memory from /proc/meminfo, in bytes.
type kernelMemory struct {
	slab, pageTables, stacks, percpu uint64
}

func parseKernelMemory(meminfo []byte) kernelMemory {
	return kernelMemory{
		slab:       field(meminfo, "SUnreclaim:") * 1024,
		pageTables: (field(meminfo, "PageTables:") + field(meminfo, "SecPageTables:")) * 1024,
		stacks:     field(meminfo, "KernelStack:") * 1024,
		percpu:     field(meminfo, "Percpu:") * 1024,
	}
}

// memoryParts returns the breakdown of used bytes, the biggest first, or
// nil when there is no cgroup v2 tree to read.
func (r *Reader) memoryParts(ctx context.Context, used uint64, k kernelMemory) []node.MemoryPart {
	r.setDir(r.cgroup)
	if _, err := r.read("/cgroup.controllers"); err != nil {
		return nil // not cgroup v2
	}
	r.gen++
	parts := make([]node.MemoryPart, 0, 32)
	var systemSlice, dockerDir bool
	err := r.eachDir(r.cgroup, func(name []byte) {
		switch string(name) {
		case "system.slice":
			systemSlice = true
		case "docker":
			dockerDir = true // Docker's cgroupfs driver: docker/<id>
		case "init.scope":
			parts = r.addUnit(parts, name, node.MemorySystem, "systemd")
		case "user.slice":
			parts = r.addUnit(parts, name, node.MemorySystem, "login sessions")
		default:
			parts = r.addUnit(parts, name, node.MemorySystem, "")
		}
	})
	if err != nil {
		return nil
	}
	if systemSlice {
		_ = r.eachDir(r.cgroup+"/system.slice", func(name []byte) {
			if id, ok := scopeID(name); ok {
				parts = r.addContainer(ctx, parts, name, id)
			} else if p, ok := platformUnits[string(name)]; ok {
				parts = r.addUnit(parts, name, node.MemoryPlatform, p)
			} else {
				parts = r.addUnit(parts, name, node.MemorySystem, "")
			}
		})
	}
	if dockerDir {
		_ = r.eachDir(r.cgroup+"/docker", func(name []byte) {
			parts = r.addContainer(ctx, parts, name, name)
		})
	}
	r.prune()
	if len(parts) == 0 {
		return nil
	}

	parts = foldSystem(parts)
	var attributed uint64
	for _, p := range parts {
		attributed += p.Bytes
	}
	for _, kp := range []node.MemoryPart{
		{Group: node.MemoryKernel, Name: "slab", Bytes: k.slab},
		{Group: node.MemoryKernel, Name: "page tables", Bytes: k.pageTables},
		{Group: node.MemoryKernel, Name: "kernel stacks", Bytes: k.stacks},
		{Group: node.MemoryKernel, Name: "per-CPU", Bytes: k.percpu},
	} {
		if kp.Bytes > 0 {
			parts = append(parts, kp)
			attributed += kp.Bytes
		}
	}
	if used > attributed {
		parts = append(parts, node.MemoryPart{Group: node.MemoryKernel, Name: "reserves and other", Bytes: used - attributed})
	}
	slices.SortStableFunc(parts, func(a, b node.MemoryPart) int { return cmp.Compare(b.Bytes, a.Bytes) })
	return parts
}

// addUnit adds the cgroup name (a directory in the current one) as a part
// of group, called display, or after its unit name without ".service" when
// display is empty. Parts of the same group and name are summed.
func (r *Reader) addUnit(parts []node.MemoryPart, name []byte, group node.MemoryGroup, display string) []node.MemoryPart {
	b := r.memoryStat(name)
	if b == 0 {
		return parts
	}
	if display == "" {
		o := r.units[string(name)]
		if o == nil {
			o = &owner{part: node.MemoryPart{Name: string(bytes.TrimSuffix(name, []byte(".service")))}}
			r.units[string(name)] = o
		}
		o.gen = r.gen
		display = o.part.Name
	}
	return add(parts, node.MemoryPart{Group: group, Name: display, Bytes: b})
}

// addContainer adds the container with this ID, whose cgroup is name in
// the current directory, as Options.Container describes it.
func (r *Reader) addContainer(ctx context.Context, parts []node.MemoryPart, name, id []byte) []node.MemoryPart {
	b := r.memoryStat(name)
	if b == 0 {
		return parts
	}
	o := r.ctrs[string(id)]
	if o == nil {
		key := string(id)
		o = &owner{part: node.MemoryPart{Group: node.MemorySystem, Name: "container " + key[:min(12, len(key))]}}
		remember := true
		if r.describe != nil {
			d, err := r.describe(ctx, key)
			if err == nil {
				o.part = node.MemoryPart{Group: d.Group, Name: d.Name, Service: d.Service}
			}
			// One that can't be described now (starting or going away)
			// is asked about again next time.
			remember = err == nil
		}
		if remember {
			r.ctrs[key] = o
		}
	}
	o.gen = r.gen
	p := o.part
	p.Bytes = b
	return add(parts, p)
}

// memoryStat reads memory.stat of the cgroup name in the current directory
// and returns anon+shmem, 0 when it can't be read.
func (r *Reader) memoryStat(name []byte) uint64 {
	dir := r.dirLen
	defer func() { r.dirLen = dir }()
	r.path = append(append(r.path[:dir], '/'), name...)
	r.dirLen = len(r.path)
	b, err := r.read("/memory.stat")
	if err != nil {
		return 0
	}
	return field(b, "anon ") + field(b, "shmem ")
}

// prune forgets cgroups the last walk didn't see.
func (r *Reader) prune() {
	for k, o := range r.units {
		if o.gen != r.gen {
			delete(r.units, k)
		}
	}
	for k, o := range r.ctrs {
		if o.gen != r.gen {
			delete(r.ctrs, k)
		}
	}
}

// add appends p, or adds its bytes to the part of the same group, name
// and service.
func add(parts []node.MemoryPart, p node.MemoryPart) []node.MemoryPart {
	for i := range parts {
		if q := &parts[i]; q.Group == p.Group && q.Name == p.Name && q.Service == p.Service {
			q.Bytes += p.Bytes
			return parts
		}
	}
	return append(parts, p)
}

// foldSystem keeps the maxSystemParts biggest MemorySystem parts and sums
// the rest into "everything else".
func foldSystem(parts []node.MemoryPart) []node.MemoryPart {
	slices.SortStableFunc(parts, func(a, b node.MemoryPart) int { return cmp.Compare(b.Bytes, a.Bytes) })
	var rest uint64
	n, kept := 0, 0
	for _, p := range parts {
		if p.Group == node.MemorySystem {
			if kept == maxSystemParts {
				rest += p.Bytes
				continue
			}
			kept++
		}
		parts[n] = p
		n++
	}
	parts = parts[:n]
	if rest > 0 {
		parts = append(parts, node.MemoryPart{Group: node.MemorySystem, Name: "everything else", Bytes: rest})
	}
	return parts
}

// scopeID returns the container ID of a "docker-<id>.scope" cgroup.
func scopeID(name []byte) ([]byte, bool) {
	id, ok := bytes.CutPrefix(name, []byte("docker-"))
	if !ok {
		return nil, false
	}
	if id, ok = bytes.CutSuffix(id, []byte(".scope")); !ok || len(id) != 64 {
		return nil, false
	}
	return id, true
}
