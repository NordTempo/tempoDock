// Package stats implements node.Stats by reading the kernel's own files:
// /proc for the host and cgroup v2 files for containers. No Docker API
// calls, so a sample of a server with 50 containers is a few hundred small
// file reads and nothing else.
package stats

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Options say where to read from. Zero values are the real locations.
type Options struct {
	// Proc is procfs, default /proc.
	Proc string
	// Cgroup is the cgroup v2 mount, default /sys/fs/cgroup.
	Cgroup string
	// Disks are the directories whose filesystems are reported: the data
	// dir and Docker's root.
	Disks []string
}

// Reader samples a server. It is safe for concurrent use; samples are
// serialised because they share one read buffer.
type Reader struct {
	proc, cgroup string
	disks        []string

	mu  sync.Mutex
	buf []byte
	// path is the file being read, built in place; its first dirLen bytes
	// are the current directory.
	path   []byte
	dirLen int
	// layout is how Docker names container cgroups here, found from the
	// first container that exists.
	layout layout
}

type layout int

const (
	_ layout = iota // not found yet
	// layoutSystemd is Docker's systemd cgroup driver (the default on
	// systemd hosts): system.slice/docker-<id>.scope.
	layoutSystemd
	// layoutCgroupfs is the cgroupfs driver: docker/<id>.
	layoutCgroupfs
)

var _ node.Stats = (*Reader)(nil)

// New returns a Reader.
func New(o Options) *Reader {
	if o.Proc == "" {
		o.Proc = "/proc"
	}
	if o.Cgroup == "" {
		o.Cgroup = "/sys/fs/cgroup"
	}
	return &Reader{proc: o.Proc, cgroup: o.Cgroup, disks: o.Disks, buf: make([]byte, 8192), path: make([]byte, 0, 256)}
}

// HostStats implements node.Stats.
func (r *Reader) HostStats(context.Context) (node.HostStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var h node.HostStats
	r.setDir(r.proc)
	b, err := r.read("/stat")
	if err != nil {
		return h, err
	}
	h.CPUBusy, h.CPUTotal, h.CPUs = parseProcStat(b)
	if b, err = r.read("/meminfo"); err != nil {
		return h, err
	}
	h.MemoryTotal, h.MemoryAvailable = parseMeminfo(b)
	if b, err = r.read("/loadavg"); err != nil {
		return h, err
	}
	h.Load1 = parseLoad1(b)
	h.Disks = disks(r.disks)
	return h, nil
}

// ContainerStats implements node.Stats. Reading a container allocates
// nothing: paths are built in one reused buffer, files read into another.
func (r *Reader) ContainerStats(_ context.Context, ids []string) ([]node.ContainerStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]node.ContainerStats, 0, len(ids))
	now := time.Now()
	for _, id := range ids {
		if !r.containerDir(id) {
			continue
		}
		s, err := r.container()
		if errors.Is(err, fs.ErrNotExist) {
			continue // gone between listing and reading
		}
		if err != nil {
			return out, err
		}
		s.ID, s.Time = id, now
		out = append(out, s)
	}
	return out, nil
}

// containerDir points the path buffer at id's cgroup directory and
// reports whether there is one.
func (r *Reader) containerDir(id string) bool {
	switch r.layout {
	case layoutSystemd:
		r.setDir(r.cgroup, "/system.slice/docker-", id, ".scope")
		return true
	case layoutCgroupfs:
		r.setDir(r.cgroup, "/docker/", id)
		return true
	}
	var st syscall.Stat_t
	if syscall.Stat(r.cgroup+"/system.slice/docker-"+id+".scope", &st) == nil {
		r.layout = layoutSystemd
		return r.containerDir(id)
	}
	if syscall.Stat(r.cgroup+"/docker/"+id, &st) == nil {
		r.layout = layoutCgroupfs
		return r.containerDir(id)
	}
	return false
}

// container reads the cgroup directory containerDir set.
func (r *Reader) container() (node.ContainerStats, error) {
	var s node.ContainerStats
	b, err := r.read("/cpu.stat")
	if err != nil {
		return s, err
	}
	s.CPUUsec = field(b, "usage_usec ")
	if b, err = r.read("/memory.current"); err != nil {
		return s, err
	}
	usage := parseUint(bytes.TrimSpace(b))
	if b, err = r.read("/memory.max"); err != nil {
		return s, err
	}
	s.MemoryLimit = parseUint(bytes.TrimSpace(b)) // "max" parses as 0: no limit
	if b, err = r.read("/memory.stat"); err != nil {
		return s, err
	}
	s.Memory = workingSet(usage, field(b, "inactive_file "))
	// io.stat is missing when the io controller isn't enabled; then there
	// is simply no IO to report.
	if b, err = r.read("/io.stat"); err == nil {
		s.IORead, s.IOWrite = parseIOStat(b)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	return s, nil
}

// setDir sets the directory read's names are relative to.
func (r *Reader) setDir(parts ...string) {
	r.path = r.path[:0]
	for _, p := range parts {
		r.path = append(r.path, p...)
	}
	r.dirLen = len(r.path)
}

// read reads a small file in the current directory into the shared
// buffer, growing it for files that don't fit. The result is valid until
// the next read.
func (r *Reader) read(name string) ([]byte, error) {
	r.path = append(append(r.path[:r.dirLen], name...), 0) // NUL-terminated for open
	fd, errno := open(r.path)
	if errno != 0 {
		return nil, r.pathError("open", errno)
	}
	defer func() { _ = syscall.Close(fd) }()
	n := 0
	for {
		m, err := syscall.Read(fd, r.buf[n:])
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return nil, r.pathError("read", err)
		}
		if m == 0 {
			return r.buf[:n], nil
		}
		n += m
		if n == len(r.buf) {
			r.buf = append(r.buf, make([]byte, len(r.buf))...)
		}
	}
}

func (r *Reader) pathError(op string, err error) error {
	return &fs.PathError{Op: op, Path: string(r.path[:len(r.path)-1]), Err: err}
}

// workingSet is memory usage without inactive page cache, which the
// kernel reclaims first; `docker stats` reports the same.
func workingSet(usage, inactiveFile uint64) uint64 {
	if inactiveFile < usage {
		return usage - inactiveFile
	}
	return usage
}

// parseProcStat reads the aggregate "cpu" line of /proc/stat: busy and
// total ticks, and the number of per-CPU lines.
func parseProcStat(b []byte) (busy, total uint64, cpus int) {
	for len(b) > 0 {
		var line []byte
		line, b, _ = bytes.Cut(b, []byte{'\n'})
		if !bytes.HasPrefix(line, []byte("cpu")) {
			break // the cpu lines come first
		}
		if len(line) > 3 && line[3] != ' ' {
			cpus++
			continue
		}
		// cpu user nice system idle iowait irq softirq steal guest guest_nice;
		// guest time is already counted in user and nice.
		for i, f := range bytes.Fields(line[3:]) {
			if i >= 8 {
				break
			}
			v := parseUint(f)
			total += v
			if i != 3 && i != 4 { // idle and iowait
				busy += v
			}
		}
	}
	return busy, total, cpus
}

// parseMeminfo returns MemTotal and MemAvailable in bytes.
func parseMeminfo(b []byte) (total, available uint64) {
	return field(b, "MemTotal:") * 1024, field(b, "MemAvailable:") * 1024
}

// parseLoad1 reads the first number of /proc/loadavg.
func parseLoad1(b []byte) float64 {
	f, _, _ := bytes.Cut(bytes.TrimSpace(b), []byte{' '})
	v, _ := strconv.ParseFloat(string(f), 64)
	return v
}

// parseIOStat sums rbytes and wbytes over the devices in io.stat.
func parseIOStat(b []byte) (read, write uint64) {
	for len(b) > 0 {
		var f []byte
		b = bytes.TrimLeft(b, " \n")
		f, b, _ = bytes.Cut(b, []byte{' '})
		if v, ok := bytes.CutPrefix(f, []byte("rbytes=")); ok {
			read += parseUint(v)
		} else if v, ok := bytes.CutPrefix(f, []byte("wbytes=")); ok {
			write += parseUint(v)
		}
	}
	return read, write
}

// field finds the line starting with key and parses the number after it.
func field(b []byte, key string) uint64 {
	for len(b) > 0 {
		var line []byte
		line, b, _ = bytes.Cut(b, []byte{'\n'})
		if v, ok := bytes.CutPrefix(line, []byte(key)); ok {
			return parseUint(bytes.TrimLeft(v, " "))
		}
	}
	return 0
}

// parseUint parses leading decimal digits; anything else (like "max") is 0.
func parseUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + uint64(c-'0')
	}
	return v
}
