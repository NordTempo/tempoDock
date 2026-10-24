package stats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	appID      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	buildkitID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// memStat is a cgroup's memory.stat; file pages are never counted.
func memStat(anon, shmem int) string {
	return fmt.Sprintf("anon %d\nfile 999999999\nkernel 12345\nshmem %d\n", anon, shmem)
}

// memoryTree is a server's /proc and cgroup tree: platform units, an app
// and the BuildKit container, and 14 system parts (four too many).
func memoryTree() map[string]string {
	files := map[string]string{
		"proc/stat":    procStat,
		"proc/loadavg": "0.1 0.1 0.1 1/100 1\n",
		"proc/meminfo": "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n" +
			"SUnreclaim: 100000 kB\nKernelStack: 5000 kB\nPageTables: 10000 kB\nSecPageTables: 0 kB\nPercpu: 1000 kB\n",
		"cgroup/cgroup.controllers":                                       "cpu memory\n",
		"cgroup/init.scope/memory.stat":                                   memStat(10<<20, 0),
		"cgroup/user.slice/memory.stat":                                   memStat(20<<20, 1<<20),
		"cgroup/system.slice/dock.service/memory.stat":                    memStat(24<<20, 0),
		"cgroup/system.slice/dock-update.service/memory.stat":             memStat(1<<20, 0),
		"cgroup/system.slice/docker.service/memory.stat":                  memStat(80<<20, 0),
		"cgroup/system.slice/containerd.service/memory.stat":              memStat(30<<20, 0),
		"cgroup/system.slice/docker-" + appID + ".scope/memory.stat":      memStat(40<<20, 8<<20),
		"cgroup/system.slice/docker-" + buildkitID + ".scope/memory.stat": memStat(300<<20, 0),
		"cgroup/system.slice/idle.service/memory.stat":                    memStat(0, 0),
	}
	for i := 1; i <= 12; i++ {
		files[fmt.Sprintf("cgroup/system.slice/unit%02d.service/memory.stat", i)] = memStat(i<<20, 0)
	}
	return files
}

func TestMemoryParts(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, memoryTree())
	described := 0
	r := New(Options{Proc: root + "/proc", Cgroup: root + "/cgroup", Container: func(_ context.Context, id string) (node.MemoryPart, error) {
		described++
		if id == appID {
			return node.MemoryPart{Group: node.MemoryApps, Name: "web-1", Service: "7"}, nil
		}
		return node.MemoryPart{Group: node.MemoryPlatform, Name: "BuildKit"}, nil
	}})
	h, err := r.HostStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]uint64{}
	var sum uint64
	system := 0
	for i, p := range h.MemoryParts {
		if i > 0 && p.Bytes > h.MemoryParts[i-1].Bytes {
			t.Errorf("parts aren't biggest first: %+v", h.MemoryParts)
		}
		got[string(p.Group)+"/"+p.Name+"/"+p.Service] = p.Bytes
		sum += p.Bytes
		if p.Group == node.MemorySystem {
			system++
		}
	}
	for key, want := range map[string]uint64{
		"apps/web-1/7":            48 << 20, // anon + shmem
		"platform/BuildKit/":      300 << 20,
		"platform/dock/":          25 << 20, // dock and dock-update
		"platform/Docker engine/": 110 << 20,
		"system/login sessions/":  21 << 20,
		"system/systemd/":         10 << 20,
		"system/unit12/":          12 << 20,
		"system/everything else/": (1 + 2 + 3 + 4) << 20, // the four smallest units
		"kernel/slab/":            100000 << 10,
		"kernel/page tables/":     10000 << 10,
		"kernel/kernel stacks/":   5000 << 10,
		"kernel/per-CPU/":         1000 << 10,
	} {
		if got[key] != want {
			t.Errorf("%s = %d, want %d", key, got[key], want)
		}
	}
	if _, ok := got["system/idle/"]; ok {
		t.Error("a unit using nothing is listed")
	}
	if system != maxSystemParts+1 {
		t.Errorf("%d system parts, want %d and everything else", system, maxSystemParts)
	}
	// What nothing explains makes the parts add up to used memory.
	if used := uint64(2000000 << 10); sum != used || got["kernel/reserves and other/"] == 0 {
		t.Errorf("parts add up to %d, want %d: %+v", sum, used, h.MemoryParts)
	}

	// Containers are described once while they exist, again after they're gone.
	if _, err := r.HostStats(context.Background()); err != nil || described != 2 {
		t.Fatalf("described %d times after two samples (%v), want 2", described, err)
	}
	app := filepath.Join(root, "cgroup/system.slice/docker-"+appID+".scope")
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	if _, err := r.HostStats(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{"cgroup/system.slice/docker-" + appID + ".scope/memory.stat": memStat(1<<20, 0)})
	if _, err := r.HostStats(context.Background()); err != nil || described != 3 {
		t.Fatalf("described %d times after the app came back (%v), want 3", described, err)
	}
}

func TestMemoryPartsWithoutCgroupV2(t *testing.T) {
	root := t.TempDir()
	files := memoryTree()
	delete(files, "cgroup/cgroup.controllers")
	writeFiles(t, root, files)
	h, err := New(Options{Proc: root + "/proc", Cgroup: root + "/cgroup"}).HostStats(context.Background())
	if err != nil || h.MemoryParts != nil {
		t.Fatalf("parts = %+v, %v; want none", h.MemoryParts, err)
	}
}

func TestScopeID(t *testing.T) {
	for name, want := range map[string]string{
		"docker-" + appID + ".scope": appID,
		"docker-abc.scope":           "",
		"docker.service":             "",
		"session-1.scope":            "",
	} {
		id, ok := scopeID([]byte(name))
		if string(id) != want || ok != (want != "") {
			t.Errorf("scopeID(%q) = %q, %v", name, id, ok)
		}
	}
}
