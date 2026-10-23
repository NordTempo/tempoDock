package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Wire types: what crosses the link, one request and one result type per
// operation. They are separate from the node package's structs so that
// renaming a Go field can't break an older node: JSON names are the
// contract. Within one ALPN major version they only ever gain optional
// fields (ADR 0014 section 3). Durations travel as milliseconds.

// Ingress is how a server receives public traffic (ADR 0013).
type Ingress string

const (
	IngressDirect           Ingress = "direct"
	IngressCloudflareTunnel Ingress = "cloudflare_tunnel"
)

// Facts are what a node reports about its machine in every hello. The
// node daemon gathers them (Node.Facts); the control plane stores them on
// the server's row.
type Facts struct {
	DockerVersion string  `json:"docker_version"`
	Ingress       Ingress `json:"ingress"`
	// PublicIPs are what DNS should point at (direct ingress only).
	PublicIPs []string `json:"public_ips,omitempty"`
	// TunnelHostname is the CNAME target, <tunnel-id>.cfargotunnel.com
	// (cloudflare_tunnel ingress only).
	TunnelHostname string `json:"tunnel_hostname,omitempty"`
	// Builder is true when the node runs BuildKit for builds.
	Builder bool `json:"builder"`
	// MemoryTotal is the machine's memory in bytes, for placement.
	MemoryTotal uint64 `json:"memory_total"`
	// PlainHTTP is true when the node's Caddy serves apps over http://
	// only ([caddy] disable_tls in direct mode): no certificates to wait
	// for, and app URLs are http://.
	PlainHTTP bool `json:"plain_http,omitempty"`
	// BaseDomain is the node config's base_domain (BASE_DOMAIN at install).
	// The control plane takes it when the server joins without one; after
	// that `dock server update --base-domain` decides.
	BaseDomain string `json:"base_domain,omitempty"`
}

// Hello is what the control plane learns about a node each time it
// connects.
type Hello struct {
	// Version is the node's dock binary version.
	Version string `json:"version"`
	// Protocol is the node's minor protocol version.
	Protocol int `json:"protocol"`
	// Arch is the node binary's GOARCH (protocol 4): which release binary
	// an update sends it.
	Arch string `json:"arch,omitempty"`
	Facts
}

// helloRequest is the control plane's side of hello: the node logs a
// clear error when it is too old for the control plane.
type helloRequest struct {
	Version     string `json:"version"`
	Protocol    int    `json:"protocol"`
	MinProtocol int    `json:"min_protocol"`
}

// Joining mode (join.go).
type (
	joinResult struct {
		Hello  Hello  `json:"hello"`
		Secret []byte `json:"secret"`
	}
	joinedRequest struct {
		ServerID int64  `json:"server_id,omitempty"`
		Server   string `json:"server,omitempty"`
		// Error, when set, says why the join was refused.
		Error string `json:"error,omitempty"`
	}
	rotateResult struct {
		Fingerprint string `json:"fingerprint"`
	}
	// updateRequest is the update call's arguments (protocol 4, ADR
	// 0028). The files follow in the body, back to back, in this order.
	updateRequest struct {
		Version     string       `json:"version"`
		RequestedBy string       `json:"requested_by"`
		Files       []updateFile `json:"files"`
	}
	updateFile struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
)

// Requests and results of the Runtime operations.
type (
	empty        struct{}
	imageRequest struct {
		Ref string `json:"ref"`
	}
	pullRequest struct {
		Ref string `json:"ref"`
		// Token is the registry token for this pull; empty is anonymous.
		Token string `json:"token,omitempty"`
	}
	imageIDResult struct {
		ImageID string `json:"image_id"`
	}
	hasImageResult struct {
		Present bool `json:"present"`
	}
	resourceRequest struct {
		Name   string     `json:"name"`
		Labels wireLabels `json:"labels"`
	}
	startRequest struct {
		Spec wireSpec `json:"spec"`
	}
	containerRequest struct {
		ID string `json:"id"`
	}
	stopRequest struct {
		ID        string `json:"id"`
		TimeoutMS int64  `json:"timeout_ms"`
	}
	containerResult struct {
		Container wireContainer `json:"container"`
	}
	containersResult struct {
		Containers capped[wireContainer] `json:"containers"`
	}
	// releaseRequest asks for a release's containers. Labels narrows them
	// (node.Labels.Matches); control planes before it sent only the ID.
	releaseRequest struct {
		ReleaseID int64       `json:"release_id"`
		Labels    *wireLabels `json:"labels,omitempty"`
	}
	idsResult struct {
		IDs capped[string] `json:"ids"`
	}
	healthRequest struct {
		Container wireContainer   `json:"container"`
		Check     wireHealthCheck `json:"check"`
	}
	logsRequest struct {
		ID     string    `json:"id"`
		Follow bool      `json:"follow,omitempty"`
		Tail   int       `json:"tail,omitempty"`
		Since  time.Time `json:"since,omitzero"`
	}
	buildRequest struct {
		Dockerfile   string            `json:"dockerfile,omitempty"`
		BuildCommand string            `json:"build_command,omitempty"`
		StartCommand string            `json:"start_command,omitempty"`
		BuildArgs    map[string]string `json:"build_args,omitempty"`
		CacheScope   string            `json:"cache_scope,omitempty"`
		Tag          string            `json:"tag"`
		Push         *wirePushTarget   `json:"push,omitempty"`
	}
	routesRequest struct {
		Routes []wireRoute `json:"routes"`
	}
	domainRequest struct {
		Hostname string `json:"hostname"`
	}
	containerStatsRequest struct {
		IDs []string `json:"ids"`
	}
	pruneImagesRequest struct {
		Keep  []string `json:"keep"`
		Repos []string `json:"repos,omitempty"` // protocol 3
	}
	pruneCacheRequest struct {
		KeepBytes int64 `json:"keep_bytes"`
	}
	pruneResult struct {
		Removed        int   `json:"removed"`
		ReclaimedBytes int64 `json:"reclaimed_bytes"`
	}
	containerStatsResult struct {
		Stats capped[wireContainerStats] `json:"stats"`
	}
	dialRequest struct {
		ContainerID string `json:"container_id"`
		Port        int    `json:"port"`
	}
)

// capped is a list in a node's answer: decoding fails with errTooMany past
// maxListItems elements, before they are allocated. It encodes like a
// plain slice.
type capped[T any] []T

// UnmarshalJSON implements json.Unmarshaler, one element at a time.
func (l *capped[T]) UnmarshalJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	tok, err := d.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		*l = nil
		return nil
	}
	if tok != json.Delim('[') {
		return fmt.Errorf("want a list, got %v", tok)
	}
	out := (*l)[:0]
	for d.More() {
		if len(out) == maxListItems {
			return errTooMany
		}
		var v T
		if err := d.Decode(&v); err != nil {
			return err
		}
		out = append(out, v)
	}
	if _, err := d.Token(); err != nil { // the closing ]
		return err
	}
	*l = out
	return nil
}

type wirePushTarget struct {
	Ref   string `json:"ref"`
	Token string `json:"token"`
}

func toWirePushTarget(p *node.PushTarget) *wirePushTarget {
	if p == nil {
		return nil
	}
	return &wirePushTarget{Ref: p.Ref, Token: p.Token}
}

func (w *wirePushTarget) node() *node.PushTarget {
	if w == nil {
		return nil
	}
	return &node.PushTarget{Ref: w.Ref, Token: w.Token}
}

type wireLabels struct {
	ControlPlane string `json:"control_plane,omitempty"`
	Project      string `json:"project,omitempty"`
	Environment  string `json:"environment,omitempty"`
	Service      string `json:"service,omitempty"`
	Release      string `json:"release,omitempty"`
}

func toWireLabels(l node.Labels) wireLabels {
	return wireLabels{ControlPlane: l.ControlPlane, Project: l.Project, Environment: l.Environment, Service: l.Service, Release: l.Release}
}

func (w wireLabels) node() node.Labels {
	return node.Labels{ControlPlane: w.ControlPlane, Project: w.Project, Environment: w.Environment, Service: w.Service, Release: w.Release}
}

type wireMount struct {
	Volume   string `json:"volume"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

func toWireMounts(ms []node.Mount) []wireMount {
	var out []wireMount
	for _, m := range ms {
		out = append(out, wireMount{Volume: m.Volume, Path: m.Path, ReadOnly: m.ReadOnly})
	}
	return out
}

func fromWireMounts(ms []wireMount) []node.Mount {
	var out []node.Mount
	for _, m := range ms {
		out = append(out, node.Mount{Volume: m.Volume, Path: m.Path, ReadOnly: m.ReadOnly})
	}
	return out
}

type wireSpec struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Labels      wireLabels        `json:"labels"`
	Env         map[string]string `json:"env,omitempty"`
	Cmd         string            `json:"cmd,omitempty"`
	Network     string            `json:"network,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
	Port        int               `json:"port,omitempty"`
	HostPort    int               `json:"host_port,omitempty"`
	Mounts      []wireMount       `json:"mounts,omitempty"`
	MemoryBytes int64             `json:"memory_bytes,omitempty"`
	NanoCPUs    int64             `json:"nano_cpus,omitempty"`
}

func toWireSpec(s node.ContainerSpec) wireSpec {
	w := wireSpec{
		Name: s.Name, Image: s.Image, Labels: toWireLabels(s.Labels), Env: s.Env, Cmd: s.Cmd,
		Network: s.Network, Aliases: s.Aliases, Port: s.Port, HostPort: s.HostPort,
		MemoryBytes: s.Resources.MemoryBytes, NanoCPUs: s.Resources.NanoCPUs, Mounts: toWireMounts(s.Mounts),
	}
	return w
}

func (w wireSpec) node() node.ContainerSpec {
	s := node.ContainerSpec{
		Name: w.Name, Image: w.Image, Labels: w.Labels.node(), Env: w.Env, Cmd: w.Cmd,
		Network: w.Network, Aliases: w.Aliases, Port: w.Port, HostPort: w.HostPort,
		Resources: node.Resources{MemoryBytes: w.MemoryBytes, NanoCPUs: w.NanoCPUs}, Mounts: fromWireMounts(w.Mounts),
	}
	return s
}

// jobRequest is RunJob's argument (protocol 2).
type jobRequest struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Labels      wireLabels        `json:"labels"`
	Env         map[string]string `json:"env,omitempty"`
	Script      string            `json:"script"`
	Network     string            `json:"network,omitempty"`
	Mounts      []wireMount       `json:"mounts,omitempty"`
	MemoryBytes int64             `json:"memory_bytes,omitempty"`
	NanoCPUs    int64             `json:"nano_cpus,omitempty"`
}

func toJobRequest(s node.JobSpec) jobRequest {
	return jobRequest{
		Name: s.Name, Image: s.Image, Labels: toWireLabels(s.Labels), Env: s.Env, Script: s.Script,
		Network: s.Network, Mounts: toWireMounts(s.Mounts),
		MemoryBytes: s.Resources.MemoryBytes, NanoCPUs: s.Resources.NanoCPUs,
	}
}

func (w jobRequest) node() node.JobSpec {
	return node.JobSpec{
		Name: w.Name, Image: w.Image, Labels: w.Labels.node(), Env: w.Env, Script: w.Script,
		Network: w.Network, Mounts: fromWireMounts(w.Mounts),
		Resources: node.Resources{MemoryBytes: w.MemoryBytes, NanoCPUs: w.NanoCPUs},
	}
}

// jobResult is RunJob's answer. Output is at most node.MaxJobOutput
// bytes, so the frame stays under maxMessage unless it is mostly bytes
// JSON escapes; the node trims it further if needed (trimJobOutput).
type jobResult struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
}

type wireContainer struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Image        string     `json:"image"`
	Labels       wireLabels `json:"labels"`
	State        string     `json:"state"`
	HostPort     int        `json:"host_port,omitempty"`
	StartedAt    time.Time  `json:"started_at,omitzero"`
	ExitCode     int        `json:"exit_code,omitempty"`
	RestartCount int        `json:"restart_count,omitempty"`
	OOMKilled    bool       `json:"oom_killed,omitempty"`
}

func toWireContainer(c node.Container) wireContainer {
	return wireContainer{
		ID: c.ID, Name: c.Name, Image: c.Image, Labels: toWireLabels(c.Labels), State: string(c.State),
		HostPort: c.HostPort, StartedAt: c.StartedAt, ExitCode: c.ExitCode, RestartCount: c.RestartCount,
		OOMKilled: c.OOMKilled,
	}
}

func (w wireContainer) node() node.Container {
	return node.Container{
		ID: w.ID, Name: w.Name, Image: w.Image, Labels: w.Labels.node(), State: node.ContainerState(w.State),
		HostPort: w.HostPort, StartedAt: w.StartedAt, ExitCode: w.ExitCode, RestartCount: w.RestartCount,
		OOMKilled: w.OOMKilled,
	}
}

func toWireContainers(cs []node.Container) []wireContainer {
	out := make([]wireContainer, len(cs))
	for i, c := range cs {
		out[i] = toWireContainer(c)
	}
	return out
}

func fromWireContainers(ws []wireContainer) []node.Container {
	out := make([]node.Container, len(ws))
	for i, w := range ws {
		out[i] = w.node()
	}
	return out
}

type wireHealthCheck struct {
	Path       string `json:"path,omitempty"`
	Port       int    `json:"port,omitempty"`
	TimeoutMS  int64  `json:"timeout_ms,omitempty"`
	IntervalMS int64  `json:"interval_ms,omitempty"`
	GraceMS    int64  `json:"grace_ms,omitempty"`
}

func toWireHealthCheck(hc node.HealthCheck) wireHealthCheck {
	return wireHealthCheck{
		Path: hc.Path, Port: hc.Port, TimeoutMS: hc.Timeout.Milliseconds(), IntervalMS: hc.Interval.Milliseconds(),
		GraceMS: hc.Grace.Milliseconds(),
	}
}

func (w wireHealthCheck) node() node.HealthCheck {
	return node.HealthCheck{Path: w.Path, Port: w.Port, Timeout: ms(w.TimeoutMS), Interval: ms(w.IntervalMS), Grace: ms(w.GraceMS)}
}

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

type wireLogLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

func toWireLogLine(l node.LogLine) wireLogLine {
	return wireLogLine{Time: l.Time, Stream: l.Stream, Text: truncate(l.Text, maxLogText)}
}

func (w wireLogLine) node() node.LogLine {
	return node.LogLine{Time: w.Time, Stream: w.Stream, Text: w.Text}
}

type wireEvent struct {
	ContainerID string     `json:"container_id"`
	Labels      wireLabels `json:"labels"`
	Action      string     `json:"action"`
	ExitCode    int        `json:"exit_code,omitempty"`
	Time        time.Time  `json:"time"`
}

func toWireEvent(e node.ContainerEvent) wireEvent {
	return wireEvent{ContainerID: e.ContainerID, Labels: toWireLabels(e.Labels), Action: e.Action, ExitCode: e.ExitCode, Time: e.Time}
}

func (w wireEvent) node() node.ContainerEvent {
	return node.ContainerEvent{ContainerID: w.ContainerID, Labels: w.Labels.node(), Action: w.Action, ExitCode: w.ExitCode, Time: w.Time}
}

type wireRoute struct {
	Hostnames  []string `json:"hostnames"`
	Upstream   string   `json:"upstream,omitempty"`
	RedirectTo string   `json:"redirect_to,omitempty"`
	PathPrefix string   `json:"path_prefix,omitempty"`
	// Access is new in protocol 5 (AccessProtocol).
	Access *wireAccess `json:"access,omitempty"`
}

// wireAccess is node.Access; the key travels as base64.
type wireAccess struct {
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	Key          []byte `json:"key,omitempty"`
	LoginURL     string `json:"login_url,omitempty"`
}

func toWireRoutes(rs []node.Route) []wireRoute {
	out := make([]wireRoute, len(rs))
	for i, r := range rs {
		out[i] = wireRoute{Hostnames: r.Hostnames, Upstream: r.Upstream, RedirectTo: r.RedirectTo, PathPrefix: r.PathPrefix}
		if a := r.Access; a != nil {
			out[i].Access = &wireAccess{Username: a.Username, PasswordHash: a.PasswordHash, Key: a.Key, LoginURL: a.LoginURL}
		}
	}
	return out
}

func fromWireRoutes(ws []wireRoute) []node.Route {
	out := make([]node.Route, len(ws))
	for i, w := range ws {
		out[i] = node.Route{Hostnames: w.Hostnames, Upstream: w.Upstream, RedirectTo: w.RedirectTo, PathPrefix: w.PathPrefix}
		if a := w.Access; a != nil {
			out[i].Access = &node.Access{Username: a.Username, PasswordHash: a.PasswordHash, Key: a.Key, LoginURL: a.LoginURL}
		}
	}
	return out
}

type wireDomainCheck struct {
	ExpectedIPs   capped[string] `json:"expected_ips,omitempty"`
	ResolvedIPs   capped[string] `json:"resolved_ips,omitempty"`
	DNSOK         bool           `json:"dns_ok"`
	CertificateOK bool           `json:"certificate_ok"`
	Error         string         `json:"error,omitempty"`
}

func toWireDomainCheck(d node.DomainCheck) wireDomainCheck {
	return wireDomainCheck{ExpectedIPs: d.ExpectedIPs, ResolvedIPs: d.ResolvedIPs, DNSOK: d.DNSOK, CertificateOK: d.CertificateOK, Error: d.Error}
}

func (w wireDomainCheck) node() node.DomainCheck {
	return node.DomainCheck{ExpectedIPs: w.ExpectedIPs, ResolvedIPs: w.ResolvedIPs, DNSOK: w.DNSOK, CertificateOK: w.CertificateOK, Error: w.Error}
}

type wireDisk struct {
	Paths capped[string] `json:"paths"`
	Total uint64         `json:"total"`
	Used  uint64         `json:"used"`
}

type wireHostStats struct {
	CPUBusy         uint64           `json:"cpu_busy"`
	CPUTotal        uint64           `json:"cpu_total"`
	CPUs            int              `json:"cpus"`
	MemoryTotal     uint64           `json:"memory_total"`
	MemoryAvailable uint64           `json:"memory_available"`
	Load1           float64          `json:"load1"`
	Disks           capped[wireDisk] `json:"disks,omitempty"`
}

func toWireHostStats(h node.HostStats) wireHostStats {
	w := wireHostStats{
		CPUBusy: h.CPUBusy, CPUTotal: h.CPUTotal, CPUs: h.CPUs, MemoryTotal: h.MemoryTotal,
		MemoryAvailable: h.MemoryAvailable, Load1: h.Load1,
	}
	for _, d := range h.Disks {
		w.Disks = append(w.Disks, wireDisk{Paths: d.Paths, Total: d.Total, Used: d.Used})
	}
	return w
}

func (w wireHostStats) node() node.HostStats {
	h := node.HostStats{
		CPUBusy: w.CPUBusy, CPUTotal: w.CPUTotal, CPUs: w.CPUs, MemoryTotal: w.MemoryTotal,
		MemoryAvailable: w.MemoryAvailable, Load1: w.Load1,
	}
	for _, d := range w.Disks {
		h.Disks = append(h.Disks, node.Disk{Paths: d.Paths, Total: d.Total, Used: d.Used})
	}
	return h
}

type wireContainerStats struct {
	ID          string    `json:"id"`
	Time        time.Time `json:"time"`
	CPUUsec     uint64    `json:"cpu_usec"`
	Memory      uint64    `json:"memory"`
	MemoryLimit uint64    `json:"memory_limit,omitempty"`
	IORead      uint64    `json:"io_read"`
	IOWrite     uint64    `json:"io_write"`
}

func toWireContainerStats(ss []node.ContainerStats) []wireContainerStats {
	out := make([]wireContainerStats, len(ss))
	for i, s := range ss {
		out[i] = wireContainerStats{ID: s.ID, Time: s.Time, CPUUsec: s.CPUUsec, Memory: s.Memory, MemoryLimit: s.MemoryLimit, IORead: s.IORead, IOWrite: s.IOWrite}
	}
	return out
}

func fromWireContainerStats(ws []wireContainerStats) []node.ContainerStats {
	out := make([]node.ContainerStats, len(ws))
	for i, w := range ws {
		out[i] = node.ContainerStats{ID: w.ID, Time: w.Time, CPUUsec: w.CPUUsec, Memory: w.Memory, MemoryLimit: w.MemoryLimit, IORead: w.IORead, IOWrite: w.IOWrite}
	}
	return out
}
