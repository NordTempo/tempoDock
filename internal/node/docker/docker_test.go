package docker

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/jsonstream"
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
	o := createOptions(spec)

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

func TestCreateOptionsMinimal(t *testing.T) {
	o := createOptions(node.ContainerSpec{Name: "x", Image: "alpine"})
	if o.Config.Cmd != nil || o.Config.ExposedPorts != nil || o.HostConfig.PortBindings != nil || o.NetworkingConfig != nil {
		t.Errorf("unexpected defaults: %+v %+v %+v", o.Config, o.HostConfig, o.NetworkingConfig)
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
	if c != want {
		t.Errorf("got %+v\nwant %+v", c, want)
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
		want string
	}{
		{"running", container.State{Status: container.StateRunning}, 0, ""},
		{"exited", container.State{Status: container.StateExited, ExitCode: 1}, 0, "container exited with code 1 during health check"},
		{"restarting", container.State{Status: container.StateRestarting, ExitCode: 2}, 0, "container exited with code 2 during health check"},
		{"oom", container.State{Status: container.StateExited, ExitCode: 137, OOMKilled: true}, 0, "container ran out of memory and was killed (exit code 137) during health check"},
		{"restarted", container.State{Status: container.StateRunning}, 1, "container restarted during health check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := crashed(container.InspectResponse{State: &tc.st, RestartCount: tc.rc}, 0)
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
}
