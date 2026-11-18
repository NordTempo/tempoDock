package docker

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/NordTempo/tempoDock/internal/node"
)

func TestCreateOptions(t *testing.T) {
	spec := node.ContainerSpec{
		Name:      "web-1",
		Image:     "nginx:alpine",
		Labels:    node.Labels{Project: "p1", Service: "s1"},
		Env:       map[string]string{"B": "2", "A": "1"},
		Cmd:       "nginx -g 'daemon off;'",
		Network:   "env-net",
		Aliases:   []string{"web"},
		Port:      8080,
		Mounts:    []node.Mount{{Volume: "data", Path: "/data"}},
		Resources: node.Resources{MemoryBytes: 256 << 20, NanoCPUs: 5e8},
	}
	o := createOptions(spec, "", nil)

	if got := o.Config.Env; !slices.Equal(got, []string{"A=1", "B=2"}) {
		t.Errorf("Env = %v", got)
	}
	if got := o.Config.Cmd; !slices.Equal(got, []string{"sh", "-c", spec.Cmd}) {
		t.Errorf("Cmd = %v", got)
	}
	if o.Config.Labels[node.LabelManaged] != "true" || o.Config.Labels[node.LabelProject] != "p1" {
		t.Errorf("Labels = %v", o.Config.Labels)
	}
	h := o.HostConfig
	if h.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Errorf("RestartPolicy = %v", h.RestartPolicy)
	}
	if h.LogConfig.Type != "local" || h.LogConfig.Config["max-size"] == "" {
		t.Errorf("LogConfig = %v", h.LogConfig)
	}
	if h.Memory != spec.Resources.MemoryBytes || h.NanoCPUs != spec.Resources.NanoCPUs {
		t.Errorf("Resources = %d, %d", h.Memory, h.NanoCPUs)
	}
	if h.MemorySwap != h.Memory+1 {
		t.Errorf("MemorySwap = %d, want %d (no swap)", h.MemorySwap, h.Memory+1)
	}
	if !slices.Contains(h.SecurityOpt, "no-new-privileges") {
		t.Errorf("SecurityOpt = %v", h.SecurityOpt)
	}
	if len(h.Mounts) != 1 || h.Mounts[0].Source != "data" || h.Mounts[0].Target != "/data" {
		t.Errorf("Mounts = %v", h.Mounts)
	}
	port := network.MustParsePort("8080/tcp")
	if b := h.PortBindings[port]; len(b) != 1 || b[0].HostIP.String() != "127.0.0.1" || b[0].HostPort != "" {
		t.Errorf("PortBindings = %v", h.PortBindings)
	}
	if ep := o.NetworkingConfig.EndpointsConfig["env-net"]; ep == nil || !slices.Equal(ep.Aliases, []string{"web"}) {
		t.Errorf("EndpointsConfig = %v", o.NetworkingConfig.EndpointsConfig)
	}
}

// A spec with Args (a start command in exec form) runs them as they are,
// without a shell.
func TestCreateOptionsArgs(t *testing.T) {
	o := createOptions(node.ContainerSpec{Name: "w", Image: "ak", Args: []string{"worker", "--x"}, Cmd: "ignored"}, "", nil)
	if got := o.Config.Cmd; !slices.Equal(got, []string{"worker", "--x"}) {
		t.Errorf("Cmd = %v", got)
	}
}

// App containers run under Docker's init unless PID 1 is an init of the
// image's own.
func TestOwnInit(t *testing.T) {
	for _, c := range []struct {
		entrypoint, cmd, imageCmd []string
		want                      bool
	}{
		{nil, nil, []string{"node", "server.js"}, false},
		{[]string{"docker-entrypoint.sh"}, nil, []string{"node"}, false},
		{nil, []string{"sh", "-c", "npm start"}, []string{"/init"}, false}, // Dock's command runs, not the image's
		{[]string{"/init"}, nil, nil, true},                                // s6-overlay must be PID 1
		{nil, nil, []string{"/sbin/init"}, true},
		{[]string{"/sbin/tini", "--"}, nil, []string{"app"}, true},
		{[]string{"/usr/bin/dumb-init", "--"}, nil, nil, true},
		{nil, nil, nil, false},
	} {
		if got := ownInit(c.entrypoint, c.cmd, c.imageCmd); got != c.want {
			t.Errorf("ownInit(%q, %q, %q) = %v, want %v", c.entrypoint, c.cmd, c.imageCmd, got, c.want)
		}
	}
}

// A spec with Health gets Docker's healthcheck running the bind-mounted
// probe, which times out a second before Docker would kill it. Without an
// installed probe, or without Health, the image's own check is turned off.
func TestCreateOptionsHealth(t *testing.T) {
	spec := node.ContainerSpec{Name: "x", Image: "alpine", Health: &node.LivenessCheck{
		Port: 8080, Path: "/up", Interval: 30 * time.Second, Timeout: 5 * time.Second, Retries: 3,
		StartPeriod: time.Minute, StartInterval: 2 * time.Second,
	}}
	o := createOptions(spec, "/var/lib/dock/probe/dock-probe", nil)
	want := container.HealthConfig{
		Test:     []string{"CMD", "/.dock-probe", "http", "8080", "/up", "5000"},
		Interval: 30 * time.Second, Timeout: 6 * time.Second, Retries: 3, StartPeriod: time.Minute, StartInterval: 2 * time.Second,
	}
	if hc := o.Config.Healthcheck; hc == nil || !reflect.DeepEqual(*hc, want) {
		t.Errorf("Healthcheck = %+v, want %+v", hc, want)
	}
	if m := o.HostConfig.Mounts; len(m) != 1 || m[0].Type != mount.TypeBind || m[0].Source != "/var/lib/dock/probe/dock-probe" || m[0].Target != "/.dock-probe" || !m[0].ReadOnly {
		t.Errorf("Mounts = %+v", m)
	}

	spec.Health.Path = ""
	if hc := createOptions(spec, "/p", nil).Config.Healthcheck; !slices.Equal(hc.Test, []string{"CMD", "/.dock-probe", "tcp", "8080", "5000"}) {
		t.Errorf("tcp Test = %v", hc.Test)
	}
	spec.Health.Kind = node.HealthPostgres
	if hc := createOptions(spec, "/p", nil).Config.Healthcheck; !slices.Equal(hc.Test, []string{"CMD", "/.dock-probe", "pg", "8080", "5000"}) {
		t.Errorf("pg Test = %v", hc.Test)
	}
	// Without a probe, or without a check of Dock's (a worker, the check
	// turned off), the image's own HEALTHCHECK is turned off: core acts
	// on what Docker reports, so it must only ever report Dock's check.
	none := []string{"NONE"}
	if o := createOptions(spec, "", nil); o.Config.Healthcheck == nil || !slices.Equal(o.Config.Healthcheck.Test, none) || o.HostConfig.Mounts != nil {
		t.Errorf("without a probe: Healthcheck %+v, Mounts %+v", o.Config.Healthcheck, o.HostConfig.Mounts)
	}
	spec.Health = nil
	if o := createOptions(spec, "/p", nil); o.Config.Healthcheck == nil || !slices.Equal(o.Config.Healthcheck.Test, none) || o.HostConfig.Mounts != nil {
		t.Errorf("without a check: Healthcheck %+v, Mounts %+v", o.Config.Healthcheck, o.HostConfig.Mounts)
	}
}

func TestToContainer(t *testing.T) {
	started := "2026-10-21T10:00:00.123456789Z"
	c := toContainer(container.InspectResponse{
		ID:           "abc",
		Name:         "/web-1",
		RestartCount: 2,
		Config: &container.Config{
			Image:  "nginx:alpine",
			Labels: map[string]string{node.LabelManaged: "true", node.LabelService: "s1"},
		},
		State: &container.State{Status: container.StateExited, ExitCode: 137, OOMKilled: true, StartedAt: started},
		NetworkSettings: &container.NetworkSettings{Ports: network.PortMap{
			network.MustParsePort("80/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "49153"}},
		}},
	})
	want := node.Container{
		ID: "abc", Name: "web-1", Image: "nginx:alpine",
		Labels:   node.Labels{Service: "s1"},
		State:    node.StateExited,
		HostPort: 49153, ExitCode: 137, RestartCount: 2, OOMKilled: true,
		StartedAt: time.Date(2026, 10, 21, 10, 0, 0, 123456789, time.UTC),
	}
	if !c.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v", c.StartedAt)
	}
	c.StartedAt = want.StartedAt
	if !reflect.DeepEqual(c, want) {
		t.Errorf("got %+v\nwant %+v", c, want)
	}
}

// An unhealthy container reports what its newest failed probe said.
func TestToContainerHealth(t *testing.T) {
	long := strings.Repeat("x", 300)
	c := toContainer(container.InspectResponse{State: &container.State{Status: container.StateRunning, Health: &container.Health{
		Status: container.Unhealthy,
		Log: []*container.HealthcheckResult{
			{ExitCode: 1, Output: "old"}, {ExitCode: 1, Output: "GET /up: timed out\n"}, {ExitCode: 0, Output: ""},
		},
	}}})
	if c.Health != node.HealthUnhealthy || c.HealthOutput != "GET /up: timed out" {
		t.Errorf("Health = %q, %q", c.Health, c.HealthOutput)
	}
	c = toContainer(container.InspectResponse{State: &container.State{Health: &container.Health{
		Status: container.Unhealthy, Log: []*container.HealthcheckResult{{ExitCode: -1, Output: long}},
	}}})
	if len(c.HealthOutput) != 256 {
		t.Errorf("HealthOutput is %d bytes, want 256", len(c.HealthOutput))
	}
	c = toContainer(container.InspectResponse{State: &container.State{Health: &container.Health{
		Status: container.Healthy, Log: []*container.HealthcheckResult{{ExitCode: 1, Output: "blip"}},
	}}})
	if c.Health != node.HealthHealthy || c.HealthOutput != "" {
		t.Errorf("healthy: %q, %q", c.Health, c.HealthOutput)
	}
}

func TestToContainerNeverStarted(t *testing.T) {
	c := toContainer(container.InspectResponse{
		State: &container.State{Status: container.StateCreated, StartedAt: "0001-01-01T00:00:00Z"},
	})
	if !c.StartedAt.IsZero() || c.HostPort != 0 || c.State != node.StateCreated {
		t.Errorf("got %+v", c)
	}
}

func TestCrashed(t *testing.T) {
	cases := []struct {
		name string
		st   container.State
		rc   int
		oom  bool // an oom event was seen
		want string
	}{
		{"running", container.State{Status: container.StateRunning}, 0, false, ""},
		{"exited", container.State{Status: container.StateExited, ExitCode: 1}, 0, false, "container exited with code 1 during health check"},
		{"restarting", container.State{Status: container.StateRestarting, ExitCode: 2}, 0, false, "container exited with code 2 during health check"},
		{"oom", container.State{Status: container.StateExited, ExitCode: 137, OOMKilled: true}, 0, false, "container ran out of memory (limit 64 MB) and was killed during health check"},
		{"oom event", container.State{Status: container.StateRestarting, ExitCode: 137}, 0, true, "container ran out of memory (limit 64 MB) and was killed during health check"},
		{"restarted after oom", container.State{Status: container.StateRunning}, 1, true, "container ran out of memory (limit 64 MB) and was killed during health check"},
		{"restarted", container.State{Status: container.StateRunning}, 1, false, "container restarted during health check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := container.InspectResponse{State: &tc.st, RestartCount: tc.rc, HostConfig: &container.HostConfig{Resources: container.Resources{Memory: 64 << 20}}}
			err := crashed(st, 0, tc.oom)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPullLine(t *testing.T) {
	msgs := []jsonstream.Message{
		{Status: "Pulling from library/nginx", ID: "alpine"},
		{Status: "Pulling fs layer", ID: "l1"},
		{Status: "Downloading", ID: "l1", Progress: &jsonstream.Progress{Current: 10, Total: 100}},
		{Status: "Downloading", ID: "l1", Progress: &jsonstream.Progress{Current: 50, Total: 100}},
		{Status: "Download complete", ID: "l1"},
		{Status: "Download complete", ID: "l1"},
		{Status: "Pull complete", ID: "l1"},
		{Status: "Digest: sha256:abc"},
		{Status: "Status: Downloaded newer image for nginx:alpine"},
	}
	var got []string
	seen := map[string]string{}
	for _, m := range msgs {
		if line, ok := pullLine(m, seen); ok {
			got = append(got, line)
		}
	}
	want := []string{
		"alpine: Pulling from library/nginx",
		"l1: Pulling fs layer",
		"l1: Download complete",
		"l1: Pull complete",
		"Digest: sha256:abc",
		"Status: Downloaded newer image for nginx:alpine",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// frame builds one frame of Docker's multiplexed log stream.
func frame(stream stdcopy.StdType, payload string) []byte {
	h := make([]byte, 8)
	h[0] = byte(stream)
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func TestLineWriterDemux(t *testing.T) {
	var src bytes.Buffer
	src.Write(frame(stdcopy.Stdout, "2026-10-21T10:00:00.000000001Z hello\n"))
	src.Write(frame(stdcopy.Stderr, "2026-10-21T10:00:01Z oops\r\n"))
	// A line split across frames, then a final line without newline.
	src.Write(frame(stdcopy.Stdout, "2026-10-21T10:00:02Z sp"))
	src.Write(frame(stdcopy.Stdout, "lit\n2026-10-21T10:00:03Z tail"))

	var got []node.LogLine
	emit := func(l node.LogLine) error { got = append(got, l); return nil }
	out := &lineWriter{stream: "stdout", emit: emit}
	errw := &lineWriter{stream: "stderr", emit: emit}
	if _, err := stdcopy.StdCopy(out, errw, &src); err != nil {
		t.Fatal(err)
	}
	if err := out.flush(); err != nil {
		t.Fatal(err)
	}

	ts := func(sec, nsec int) time.Time { return time.Date(2026, 10, 21, 10, 0, sec, nsec, time.UTC) }
	want := []node.LogLine{
		{Time: ts(0, 1), Stream: "stdout", Text: "hello"},
		{Time: ts(1, 0), Stream: "stderr", Text: "oops"},
		{Time: ts(2, 0), Stream: "stdout", Text: "split"},
		{Time: ts(3, 0), Stream: "stdout", Text: "tail"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %+v", len(got), got)
	}
	for i := range want {
		if !got[i].Time.Equal(want[i].Time) || got[i].Stream != want[i].Stream || got[i].Text != want[i].Text {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A container that never writes a newline costs at most maxLogLine bytes:
// the line is cut there, on a rune boundary, and the rest of it dropped.
func TestLineWriterCapsLongLines(t *testing.T) {
	var got []node.LogLine
	w := &lineWriter{stream: "stdout", emit: func(l node.LogLine) error { got = append(got, l); return nil }}
	ts := "2026-10-21T10:00:00Z "
	long := ts + strings.Repeat("x", maxLogLine-len(ts)-1) + "é" // é straddles the cap
	for range 1000 {
		if _, err := w.Write([]byte(long)); err != nil {
			t.Fatal(err)
		}
		if len(w.buf) > maxLogLine {
			t.Fatalf("buffered %d bytes", len(w.buf))
		}
	}
	if _, err := w.Write([]byte(" end\n" + ts + "next\n")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	if want := maxLogLine - len(ts) - 1; len(got[0].Text) != want || got[0].Time.IsZero() {
		t.Errorf("cut line: %d bytes at %v, want %d bytes with its time", len(got[0].Text), got[0].Time, want)
	}
	if got[1].Text != "next" {
		t.Errorf("line after the cut = %q", got[1].Text)
	}
}

func TestToEvent(t *testing.T) {
	ev := toEvent(events.Message{
		Type:   events.ContainerEventType,
		Action: events.ActionDie,
		Actor: events.Actor{ID: "abc", Attributes: map[string]string{
			node.LabelManaged: "true", node.LabelRelease: "r1", "exitCode": "3",
		}},
		TimeNano: 1_700_000_000_000_000_001,
	})
	if ev.ContainerID != "abc" || ev.Action != "die" || ev.ExitCode != 3 || ev.Labels.Release != "r1" {
		t.Errorf("got %+v", ev)
	}
	if ev.Time.UnixNano() != 1_700_000_000_000_000_001 {
		t.Errorf("Time = %v", ev.Time)
	}
	ev = toEvent(events.Message{Action: events.ActionHealthStatusUnhealthy, Actor: events.Actor{ID: "abc"}})
	if ev.Action != "health_status" || ev.Health != node.HealthUnhealthy {
		t.Errorf("health event = %+v", ev)
	}
}

func TestFirstFree(t *testing.T) {
	p := netip.MustParsePrefix
	pool := p("10.213.0.0/16")
	tests := []struct {
		pool netip.Prefix
		used []netip.Prefix
		want string // "" means the pool is full
	}{
		{pool, nil, "10.213.0.0/24"},
		{pool, []netip.Prefix{p("172.17.0.0/16"), p("10.213.0.0/24"), p("10.213.1.0/24")}, "10.213.2.0/24"},
		{pool, []netip.Prefix{p("10.213.0.0/23"), p("fd00::/64")}, "10.213.2.0/24"},
		{pool, []netip.Prefix{p("10.213.0.128/25")}, "10.213.1.0/24"},
		{pool, []netip.Prefix{p("10.0.0.0/8")}, ""},
		{p("10.213.7.0/24"), nil, "10.213.7.0/24"},
		{p("10.213.7.0/24"), []netip.Prefix{p("10.213.7.0/24")}, ""},
		{p("10.212.254.0/23"), []netip.Prefix{p("10.212.254.0/24")}, "10.212.255.0/24"},
		{p("255.255.254.0/23"), []netip.Prefix{p("255.255.254.0/23")}, ""},
	}
	for _, tt := range tests {
		got, err := firstFree(tt.pool, tt.used)
		if tt.want == "" {
			if err == nil {
				t.Errorf("firstFree(%s, %v) = %s, want pool full", tt.pool, tt.used, got)
			}
			continue
		}
		if err != nil || got.String() != tt.want {
			t.Errorf("firstFree(%s, %v) = %s, %v; want %s", tt.pool, tt.used, got, err, tt.want)
		}
	}
}

func TestBridgeName(t *testing.T) {
	a, b := bridgeName("dock-myapp-production"), bridgeName("dock-myapp-staging")
	if a == b || len(a) > 15 || !strings.HasPrefix(a, BridgePrefix) {
		t.Errorf("bridge names %q, %q: want distinct, %s prefix, at most 15 chars", a, b, BridgePrefix)
	}
}

// Pruning only takes images the platform made: built ones (labelled) and
// ones pulled from the platform registry. Untagged images of other work on
// the host, other registries and look-alike paths are never its own.
func TestOwnedImage(t *testing.T) {
	repos := []string{"127.0.0.1:5000/dock/p"}
	for _, tc := range []struct {
		name    string
		labels  map[string]string
		digests []string
		want    bool
	}{
		{"built", map[string]string{node.LabelImage: "true"}, nil, true},
		{"from the registry", nil, []string{"127.0.0.1:5000/dock/p1_e2_s3@sha256:ab"}, true},
		{"untagged, someone else's", nil, nil, false},
		{"other dock labels", map[string]string{node.LabelManaged: "true"}, nil, false},
		{"another registry", nil, []string{"ghcr.io/acme/web@sha256:ab"}, false},
		{"same path elsewhere", nil, []string{"ghcr.io/dock/p1_e2_s3@sha256:ab"}, false},
		{"another port", nil, []string{"127.0.0.1:5001/dock/p1_e2_s3@sha256:ab"}, false},
	} {
		if got := ownedImage(tc.labels, tc.digests, repos); got != tc.want {
			t.Errorf("%s: owned = %v, want %v", tc.name, got, tc.want)
		}
	}
	if ownedImage(nil, []string{"x/y@sha256:ab"}, []string{""}) {
		t.Error("an empty repository prefix owns everything")
	}
}

// Every job of a runtime with a data dir carries its owner, not only one
// that pauses services: UnpauseStranded removes the job containers a
// crash left running by it.
func TestJobOptionsOwner(t *testing.T) {
	spec := node.JobSpec{Name: "dock_p1_e1_s1_run7", Image: "alpine", Script: "sleep 600"}
	if got := jobOptions(spec, nil, "/var/lib/dock").Config.Labels[node.LabelOwner]; got != "/var/lib/dock" {
		t.Errorf("owner label = %q, want the runtime's data dir", got)
	}
	if _, ok := jobOptions(spec, nil, "").Config.Labels[node.LabelOwner]; ok {
		t.Error("a runtime without a data dir labels its jobs with an empty owner")
	}
}
