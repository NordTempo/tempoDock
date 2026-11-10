package remote

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// TestWireRoundTrip sends every node struct that crosses the link through
// its wire type and JSON, with every field set. A field added to a node
// struct fails here until the wire type carries it.
func TestWireRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 30, 0, 123000000, time.UTC)
	labels := node.Labels{ControlPlane: "cp", Project: "1", Environment: "2", Service: "3", Release: "4"}

	check(t, node.ContainerSpec{
		Name: "web-1", Image: "app:1", Labels: labels, Env: map[string]string{"A": "1"}, Cmd: "serve",
		Network: "env-2", Aliases: []string{"web"}, Port: 8080, HostPort: 41000,
		Mounts: []node.Mount{{Volume: "data", Path: "/data", ReadOnly: true}}, Resources: node.Resources{MemoryBytes: 1 << 28, NanoCPUs: 5e8},
		Health: &node.LivenessCheck{Port: 8080, Path: "/up", Interval: 30 * time.Second, Timeout: 5 * time.Second, Retries: 3, StartPeriod: time.Minute, StartInterval: 2 * time.Second},
	}, toWireSpec, wireSpec.node)
	check(t, node.JobSpec{
		Name: "backup-1", Image: "postgres:17", Labels: labels, Env: map[string]string{"A": "1"}, Script: "pg_dump",
		Network: "env-2", Mounts: []node.Mount{{Volume: "bin", Path: "/bin", ReadOnly: true}},
		Resources: node.Resources{MemoryBytes: 1 << 28, NanoCPUs: 5e8}, Pause: []string{"abc", "def"}, Sync: true,
	}, toJobRequest, jobRequest.node, "Stdin", "Stream") // input is for the local runtime only; a stream is run-job-stream's own
	check(t, node.JobResult{ExitCode: 2, Output: "failed"},
		func(r node.JobResult) jobResult { return jobResult{ExitCode: r.ExitCode, Output: r.Output} },
		func(w jobResult) node.JobResult { return node.JobResult{ExitCode: w.ExitCode, Output: w.Output} })
	check(t, node.Container{
		ID: "abc", Name: "web-1", Image: "app:1", Labels: labels, State: node.StateRunning, HostPort: 41000,
		StartedAt: at, ExitCode: 137, RestartCount: 2, OOMKilled: true, Health: node.HealthUnhealthy, HealthOutput: "GET /up: timed out",
		AnonymousVolumes: []string{"/var/lib/postgresql/data"},
	}, toWireContainer, wireContainer.node)
	check(t, node.HealthCheck{Path: "/up", Port: 5432, Timeout: time.Minute, Interval: 2 * time.Second, Grace: 3 * time.Second},
		toWireHealthCheck, wireHealthCheck.node)
	check(t, node.LogLine{Time: at, Stream: "stderr", Text: "boom"}, toWireLogLine, wireLogLine.node)
	check(t, node.ContainerEvent{ContainerID: "abc", Labels: labels, Action: "health_status", ExitCode: 1, Health: node.HealthUnhealthy, Time: at},
		toWireEvent, wireEvent.node)
	check(t, node.Route{Hostnames: []string{"a.example.com"}, Upstream: "127.0.0.1:1", RedirectTo: "b.example.com", PathPrefix: "/p/", Traffic: true, TunnelOnly: true, Stopped: "web", Hold: true,
		Analytics: []node.AnalyticsHosts{{Hostnames: []string{"a.example.com"}, Tag: "3.40.981"}},
		Access:    &node.Access{Username: "u", PasswordHash: "$2a$06$x", Key: []byte{1}, LoginURL: "https://dock.example.com/access"}},
		func(r node.Route) []wireRoute { return toWireRoutes([]node.Route{r}) },
		func(w []wireRoute) node.Route { return fromWireRoutes(w)[0] },
		"ClientIPHeader",      // only on the control plane's own server (ADR 0029)
		"Wake", "QuietProbes") // sleeping environments: control plane's own server only, until the node link carries them
	check(t, node.DomainCheck{ExpectedIPs: []string{"1.2.3.4"}, ResolvedIPs: []string{"5.6.7.8"}, DNSOK: true, CertificateOK: true, Error: "x"},
		toWireDomainCheck, wireDomainCheck.node)
	check(t, node.HostTraffic{
		Host: "a.example.com", Requests: 1, Status: [4]uint64{2, 3, 4, 5}, Bytes: 6, Duration: 7 * time.Millisecond,
		Paths: []node.PathCount{{Method: "GET", Path: "/", Requests: 8}},
	}, func(h node.HostTraffic) []wireHostTraffic { return toWireTraffic([]node.HostTraffic{h}) },
		func(w []wireHostTraffic) node.HostTraffic { return fromWireTraffic(w)[0] })
	check(t, node.HostStats{
		CPUBusy: 1, CPUTotal: 2, CPUs: 3, MemoryTotal: 4, MemoryAvailable: 5, Load1: 0.5,
		Disks:       []node.Disk{{Paths: []string{"/var/lib/docker"}, Total: 6, Used: 7}},
		MemoryParts: []node.MemoryPart{{Group: node.MemoryApps, Name: "web-1", Service: "3", Bytes: 8}},
	}, toWireHostStats, wireHostStats.node)
	check(t, node.ContainerStats{ID: "abc", Time: at, CPUUsec: 1, Memory: 2, MemoryLimit: 3, IORead: 4, IOWrite: 5},
		func(s node.ContainerStats) []wireContainerStats {
			return toWireContainerStats([]node.ContainerStats{s})
		},
		func(w []wireContainerStats) node.ContainerStats { return fromWireContainerStats(w)[0] })
	check(t, node.LogOptions{Follow: true, Tail: 10, Since: at},
		func(o node.LogOptions) logsRequest {
			return logsRequest{Follow: o.Follow, Tail: o.Tail, Since: o.Since}
		},
		func(w logsRequest) node.LogOptions {
			return node.LogOptions{Follow: w.Follow, Tail: w.Tail, Since: w.Since}
		})
	check(t, node.PullOptions{Token: "pull", Username: "bot", Password: "s3cret"},
		func(o node.PullOptions) pullRequest {
			return pullRequest{Ref: "r", Token: o.Token, Username: o.Username, Password: o.Password}
		},
		func(w pullRequest) node.PullOptions {
			return node.PullOptions{Token: w.Token, Username: w.Username, Password: w.Password}
		})
	check(t, node.DialRequest{ContainerID: "abc", Port: 5432},
		func(d node.DialRequest) dialRequest { return dialRequest{ContainerID: d.ContainerID, Port: d.Port} },
		func(w dialRequest) node.DialRequest {
			return node.DialRequest{ContainerID: w.ContainerID, Port: w.Port}
		})

	// BuildRequest's io fields stream separately; the rest is the args.
	build := node.BuildRequest{
		Dockerfile: "Dockerfile", BuildCommand: "make", StartCommand: "run", BuildArgs: map[string]string{"A": "1"},
		CacheScope: "p1_e2", Tag: "app:1", Push: &node.PushTarget{Ref: "127.0.0.1:5000/1/2:e-3", Token: "push"},
	}
	got := jsonTrip(t, buildRequest{
		Dockerfile: build.Dockerfile, BuildCommand: build.BuildCommand, StartCommand: build.StartCommand,
		BuildArgs: build.BuildArgs, CacheScope: build.CacheScope, Tag: build.Tag, Push: toWirePushTarget(build.Push),
	})
	back := node.BuildRequest{
		Dockerfile: got.Dockerfile, BuildCommand: got.BuildCommand, StartCommand: got.StartCommand,
		BuildArgs: got.BuildArgs, CacheScope: got.CacheScope, Tag: got.Tag, Push: got.Push.node(),
	}
	allSet(t, reflect.ValueOf(build), "BuildRequest", "Source", "Logs")
	if !reflect.DeepEqual(back, build) {
		t.Fatalf("build args = %+v, want %+v", back, build)
	}
}

// check asserts that v has every field set and survives to, JSON and from.
// Fields named in skip aren't carried by the wire and may be zero.
func check[T, W any](t *testing.T, v T, to func(T) W, from func(W) T, skip ...string) {
	t.Helper()
	allSet(t, reflect.ValueOf(v), reflect.TypeOf(v).Name(), skip...)
	if got := from(jsonTrip(t, to(v))); !reflect.DeepEqual(got, v) {
		t.Fatalf("%T round trip:\n got %+v\nwant %+v", v, got, v)
	}
}

func jsonTrip[W any](t *testing.T, w W) W {
	t.Helper()
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var out W
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// allSet fails for a zero field anywhere in v, except the skipped ones.
func allSet(t *testing.T, v reflect.Value, path string, skip ...string) {
	t.Helper()
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			break
		}
	fields:
		for i := range v.NumField() {
			name := v.Type().Field(i).Name
			for _, s := range skip {
				if s == name {
					continue fields
				}
			}
			allSet(t, v.Field(i), path+"."+name)
		}
		return
	case reflect.Slice:
		if v.Len() > 0 {
			allSet(t, v.Index(0), path+"[0]")
			return
		}
	}
	if v.IsZero() {
		t.Fatalf("%s is not set in the test value", path)
	}
}

// equalJSON compares two values by their JSON, so nil and empty don't
// differ.
func equalJSON(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}
