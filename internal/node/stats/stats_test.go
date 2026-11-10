package stats

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const procStat = `cpu  1000 50 400 8000 100 20 30 0 200 0
cpu0 500 25 200 4000 50 10 15 0 100 0
cpu1 500 25 200 4000 50 10 15 0 100 0
intr 123456 0 0 0
ctxt 99
`

const meminfo = `MemTotal:       32770272 kB
MemFree:          441260 kB
MemAvailable:   24205636 kB
Buffers:          123 kB
`

const memoryStat = `anon 4096000
file 3600000
kernel 100000
active_file 1000000
inactive_file 2600000
`

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func cgroupFiles(dir, maxMem string) map[string]string {
	return map[string]string{
		dir + "/cpu.stat":       "usage_usec 47954\nuser_usec 16928\nsystem_usec 31026\n",
		dir + "/memory.current": "7778304\n",
		dir + "/memory.max":     maxMem + "\n",
		dir + "/memory.stat":    memoryStat,
		dir + "/io.stat":        "8:0 rbytes=36864 wbytes=12288 rios=5 wios=3 dbytes=0 dios=0\n259:0 rbytes=100 wbytes=4 rios=1 wios=1 dbytes=0 dios=0\n",
	}
}

func TestHostStats(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"proc/stat":    procStat,
		"proc/meminfo": meminfo,
		"proc/loadavg": "0.62 0.53 0.56 2/1045 1131362\n",
	})
	r := New(Options{Proc: root + "/proc", Cgroup: root + "/cgroup", Disks: []string{root, root + "/proc", root + "/missing"}})
	h, err := r.HostStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// user+nice+system+irq+softirq+steal; total adds idle and iowait.
	if h.CPUBusy != 1000+50+400+20+30 || h.CPUTotal != 1000+50+400+8000+100+20+30 || h.CPUs != 2 {
		t.Errorf("cpu = busy %d total %d cpus %d", h.CPUBusy, h.CPUTotal, h.CPUs)
	}
	if h.MemoryTotal != 32770272*1024 || h.MemoryAvailable != 24205636*1024 {
		t.Errorf("memory = %d / %d", h.MemoryAvailable, h.MemoryTotal)
	}
	if h.Load1 != 0.62 {
		t.Errorf("load1 = %v", h.Load1)
	}
	// Both existing paths are on one filesystem; the missing one is skipped.
	if len(h.Disks) != 1 || len(h.Disks[0].Paths) != 2 || h.Disks[0].Total == 0 || h.Disks[0].Used > h.Disks[0].Total {
		t.Errorf("disks = %+v", h.Disks)
	}
}

func TestContainerStatsLayouts(t *testing.T) {
	const id = "0b04c97da9e9675c61a38ceccd04366cee46ba91b598128130036cbfd7dd6dc6"
	for _, tc := range []struct{ name, dir string }{
		{"systemd", "system.slice/docker-" + id + ".scope"},
		{"cgroupfs", "docker/" + id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, cgroupFiles(tc.dir, "134217728"))
			r := New(Options{Cgroup: root})
			got, err := r.ContainerStats(context.Background(), []string{"gone", id})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d samples, want 1 (the missing container is left out)", len(got))
			}
			s := got[0]
			if s.ID != id || s.CPUUsec != 47954 || s.MemoryLimit != 134217728 || s.Time.IsZero() {
				t.Errorf("sample = %+v", s)
			}
			// memory.current minus inactive_file, like docker stats.
			if s.Memory != 7778304-2600000 {
				t.Errorf("memory = %d", s.Memory)
			}
			if s.IORead != 36864+100 || s.IOWrite != 12288+4 {
				t.Errorf("io = %d / %d", s.IORead, s.IOWrite)
			}
		})
	}
}

func TestContainerWithoutLimitOrIO(t *testing.T) {
	root := t.TempDir()
	dir := "system.slice/docker-abc.scope"
	files := cgroupFiles(dir, "max")
	delete(files, dir+"/io.stat")
	writeFiles(t, root, files)
	got, err := New(Options{Cgroup: root}).ContainerStats(context.Background(), []string{"abc"})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].MemoryLimit != 0 || got[0].IORead != 0 {
		t.Errorf("sample = %+v", got[0])
	}
}

func TestReadGrowsBuffer(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", 20000) + "\nusage_usec 7\n"
	writeFiles(t, root, map[string]string{"f": big})
	r := New(Options{})
	b, err := r.read(root + "/f")
	if err != nil || len(b) != len(big) || field(b, "usage_usec ") != 7 {
		t.Fatalf("read %d bytes, %v", len(b), err)
	}
}

// BenchmarkContainerStats50 is one collector tick's container reads for a
// server running 50 containers.
func BenchmarkContainerStats50(b *testing.B) {
	root := b.TempDir()
	ids := make([]string, 50)
	files := map[string]string{}
	for i := range ids {
		ids[i] = strings.Repeat("a", 62) + string(rune('a'+i/26)) + string(rune('a'+i%26))
		for k, v := range cgroupFiles("system.slice/docker-"+ids[i]+".scope", "536870912") {
			files[k] = v
		}
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	r := New(Options{Cgroup: root})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if got, err := r.ContainerStats(ctx, ids); err != nil || len(got) != 50 {
			b.Fatal(len(got), err)
		}
	}
}
