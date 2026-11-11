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
	// HTTPPort is the port the node's Caddy serves plain HTTP on: in
	// tunnel mode cloudflared forwards to 127.0.0.1 on it. 0 from nodes
	// before protocol 8, which means 80.
	HTTPPort int `json:"http_port,omitempty"`
	// TunnelPort is the port on 127.0.0.1 the node's cloudflared forwards
	// to: http_port in tunnel mode, the tunnel listener in direct mode
	// (protocol 9; 0 in direct mode before it: no tunnel there).
	TunnelPort int `json:"tunnel_port,omitempty"`
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
		// Username and Password sign in to another registry (protocol
		// 15, ADR 0051). Password is a secret.
		Username string `json:"username,omitempty"`
		Password string `json:"password,omitempty"`
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
	// openRequestsRequest is open-requests': the upstreams (host:port) to
	// count Caddy's open requests to (protocol 22).
	openRequestsRequest struct {
		Upstreams []string `json:"upstreams"`
	}
	openRequestsResult struct {
		Open int `json:"open"`
	}
	// removeRequest is remove's: a containerRequest plus whether the
	// container's anonymous volumes go too (ADR 0052). Keeping is the
	// default, so a request without the field (an older control plane)
	// never deletes data. A node from before ignores it: it removes them
	// (v0.19.0 on) or keeps them all (before).
	removeRequest struct {
		ID                   string `json:"id"`
		DropAnonymousVolumes bool   `json:"drop_anonymous_volumes,omitempty"`
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
	// archiveLogsRequest is ArchiveLogs' container and limits.
	archiveLogsRequest struct {
		ID       string `json:"id"`
		MaxBytes int64  `json:"max_bytes,omitempty"`
		MaxAgeMS int64  `json:"max_age_ms,omitempty"`
		// MaxTotalBytes is LogLimits.MaxTotalBytes.
		MaxTotalBytes int64 `json:"max_total_bytes,omitempty"`
	}
	// archiveLogsResult is node.LogArchive.
	archiveLogsResult struct {
		Service string `json:"service"`
		Release string `json:"release"`
		Bytes   int64  `json:"bytes"`
	}
	// archivedLogsRequest is node.LogArchiveQuery.
	archivedLogsRequest struct {
		Service string    `json:"service"`
		Release string    `json:"release"`
		Tail    int       `json:"tail,omitempty"`
		Since   time.Time `json:"since,omitzero"`
	}
	// pruneLogsRequest is node.LogPrune.
	pruneLogsRequest struct {
		KeepServices []string `json:"keep_services"`
		MaxBytes     int64    `json:"max_bytes,omitempty"`
		MaxAgeMS     int64    `json:"max_age_ms,omitempty"`
		// MaxTotalBytes is LogLimits.MaxTotalBytes.
		MaxTotalBytes int64 `json:"max_total_bytes,omitempty"`
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
	trafficResult struct {
		Hosts capped[wireHostTraffic] `json:"hosts"`
	}
	// wireDiskUsage is node.DiskUsage.
	wireDiskUsage struct {
		ImagesRunning     int64                   `json:"images_running"`
		ImagesKept        int64                   `json:"images_kept"`
		ImagesReclaimable int64                   `json:"images_reclaimable"`
		ImagesOther       int64                   `json:"images_other"`
		ImagesShared      int64                   `json:"images_shared"`
		Volumes           capped[wireVolumeUsage] `json:"volumes"`
		OtherVolumes      int64                   `json:"other_volumes"`
		Containers        int64                   `json:"containers"`
		DataDir           int64                   `json:"data_dir"`
	}
	wireVolumeUsage struct {
		Name         string `json:"name"`
		Service      string `json:"service,omitempty"`
		ControlPlane string `json:"control_plane,omitempty"`
		Bytes        int64  `json:"bytes"`
	}
	dialRequest struct {
		ContainerID string `json:"container_id"`
		Port        int    `json:"port"`
	}
	// probeRequest is node.ProbeRequest (protocol 19).
	probeRequest struct {
		URL          string `json:"url"`
		ExpectStatus int    `json:"expect_status,omitempty"`
		TimeoutMS    int64  `json:"timeout_ms"`
	}
	// probeResult is node.ProbeResult.
	probeResult struct {
		Up        bool   `json:"up"`
		Status    int    `json:"status,omitempty"`
		LatencyMS int64  `json:"latency_ms"`
		Error     string `json:"error,omitempty"`
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
	// Health is new in protocol 13 (healthProtocol); older nodes ignore
	// it and start the container without an ongoing check.
	Health *wireLivenessCheck `json:"health,omitempty"`
}

func toWireSpec(s node.ContainerSpec) wireSpec {
	w := wireSpec{
		Name: s.Name, Image: s.Image, Labels: toWireLabels(s.Labels), Env: s.Env, Cmd: s.Cmd,
		Network: s.Network, Aliases: s.Aliases, Port: s.Port, HostPort: s.HostPort,
		MemoryBytes: s.Resources.MemoryBytes, NanoCPUs: s.Resources.NanoCPUs, Mounts: toWireMounts(s.Mounts),
	}
	if hc := s.Health; hc != nil {
		w.Health = &wireLivenessCheck{
			Port: hc.Port, Path: hc.Path, IntervalMS: hc.Interval.Milliseconds(), TimeoutMS: hc.Timeout.Milliseconds(),
			Retries: hc.Retries, StartPeriodMS: hc.StartPeriod.Milliseconds(), StartIntervalMS: hc.StartInterval.Milliseconds(),
		}
	}
	return w
}

func (w wireSpec) node() node.ContainerSpec {
	s := node.ContainerSpec{
		Name: w.Name, Image: w.Image, Labels: w.Labels.node(), Env: w.Env, Cmd: w.Cmd,
		Network: w.Network, Aliases: w.Aliases, Port: w.Port, HostPort: w.HostPort,
		Resources: node.Resources{MemoryBytes: w.MemoryBytes, NanoCPUs: w.NanoCPUs}, Mounts: fromWireMounts(w.Mounts),
	}
	if hc := w.Health; hc != nil {
		s.Health = &node.LivenessCheck{
			Port: hc.Port, Path: hc.Path, Interval: ms(hc.IntervalMS), Timeout: ms(hc.TimeoutMS),
			Retries: hc.Retries, StartPeriod: ms(hc.StartPeriodMS), StartInterval: ms(hc.StartIntervalMS),
		}
	}
	return s
}

// wireLivenessCheck is node.LivenessCheck on the wire (protocol 13).
type wireLivenessCheck struct {
	Port            int    `json:"port"`
	Path            string `json:"path,omitempty"`
	IntervalMS      int64  `json:"interval_ms,omitempty"`
	TimeoutMS       int64  `json:"timeout_ms,omitempty"`
	Retries         int    `json:"retries,omitempty"`
	StartPeriodMS   int64  `json:"start_period_ms,omitempty"`
	StartIntervalMS int64  `json:"start_interval_ms,omitempty"`
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
	// Pause is node.JobSpec.Pause (protocol 15, SnapshotProtocol).
	Pause []string `json:"pause,omitempty"`
	// Sync is node.JobSpec.Sync (protocol 21, MoveSyncProtocol).
	Sync bool `json:"sync,omitempty"`
}

func toJobRequest(s node.JobSpec) jobRequest {
	return jobRequest{
		Name: s.Name, Image: s.Image, Labels: toWireLabels(s.Labels), Env: s.Env, Script: s.Script,
		Network: s.Network, Mounts: toWireMounts(s.Mounts),
		MemoryBytes: s.Resources.MemoryBytes, NanoCPUs: s.Resources.NanoCPUs, Pause: s.Pause, Sync: s.Sync,
	}
}

func (w jobRequest) node() node.JobSpec {
	return node.JobSpec{
		Name: w.Name, Image: w.Image, Labels: w.Labels.node(), Env: w.Env, Script: w.Script,
		Network: w.Network, Mounts: fromWireMounts(w.Mounts),
		Resources: node.Resources{MemoryBytes: w.MemoryBytes, NanoCPUs: w.NanoCPUs}, Pause: w.Pause, Sync: w.Sync,
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
	// Health and HealthOutput are new in protocol 13.
	Health       string `json:"health,omitempty"`
	HealthOutput string `json:"health_output,omitempty"`
	// AnonymousVolumes is new with ADR 0052; older nodes leave it out.
	AnonymousVolumes capped[string] `json:"anonymous_volumes,omitempty"`
}

func toWireContainer(c node.Container) wireContainer {
	return wireContainer{
		ID: c.ID, Name: c.Name, Image: c.Image, Labels: toWireLabels(c.Labels), State: string(c.State),
		HostPort: c.HostPort, StartedAt: c.StartedAt, ExitCode: c.ExitCode, RestartCount: c.RestartCount,
		OOMKilled: c.OOMKilled, Health: c.Health, HealthOutput: c.HealthOutput, AnonymousVolumes: c.AnonymousVolumes,
	}
}

func (w wireContainer) node() node.Container {
	return node.Container{
		ID: w.ID, Name: w.Name, Image: w.Image, Labels: w.Labels.node(), State: node.ContainerState(w.State),
		HostPort: w.HostPort, StartedAt: w.StartedAt, ExitCode: w.ExitCode, RestartCount: w.RestartCount,
		OOMKilled: w.OOMKilled, Health: w.Health, HealthOutput: w.HealthOutput, AnonymousVolumes: w.AnonymousVolumes,
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
	// Health is new in protocol 13: a health_status event's new state.
	Health string    `json:"health,omitempty"`
	Time   time.Time `json:"time"`
	// Request is new in protocol 22: what woke an environment, on an
	// ActionWake event (ADR 0070).
	Request string `json:"request,omitempty"`
}

func toWireEvent(e node.ContainerEvent) wireEvent {
	return wireEvent{ContainerID: e.ContainerID, Labels: toWireLabels(e.Labels), Action: e.Action, ExitCode: e.ExitCode, Health: e.Health, Time: e.Time, Request: e.Request}
}

func (w wireEvent) node() node.ContainerEvent {
	return node.ContainerEvent{ContainerID: w.ContainerID, Labels: w.Labels.node(), Action: w.Action, ExitCode: w.ExitCode, Health: w.Health, Time: w.Time, Request: w.Request}
}

type wireRoute struct {
	Hostnames  []string `json:"hostnames"`
	Upstream   string   `json:"upstream,omitempty"`
	RedirectTo string   `json:"redirect_to,omitempty"`
	PathPrefix string   `json:"path_prefix,omitempty"`
	// Access is new in protocol 5 (AccessProtocol).
	Access *wireAccess `json:"access,omitempty"`
	// Traffic is new in protocol 6 (TrafficProtocol); older nodes don't
	// count.
	Traffic bool `json:"traffic,omitempty"`
	// TunnelOnly is new in protocol 9 (TunnelListenerProtocol); routes
	// with it aren't sent to older nodes at all.
	TunnelOnly bool `json:"tunnel_only,omitempty"`
	// Analytics is new in protocol 10 (AnalyticsProtocol); older nodes
	// get routes without it.
	Analytics []wireAnalyticsHosts `json:"analytics,omitempty"`
	// Stopped is new in protocol 17 (ADR 0050); older nodes ignore it and
	// proxy to Upstream, the stopped release's port, so they answer 502.
	Stopped string `json:"stopped,omitempty"`
	// Hold is new in protocol 21 (ADR 0063); older nodes answer 502 while
	// the upstream is down instead of waiting.
	Hold bool `json:"hold,omitempty"`
	// Wake and QuietProbes are new in protocol 22 (ADR 0070); the control
	// plane sleeps no environment on an older node.
	Wake        string `json:"wake,omitempty"`
	QuietProbes bool   `json:"quiet_probes,omitempty"`
}

// wireAnalyticsHosts is node.AnalyticsHosts.
type wireAnalyticsHosts struct {
	Hostnames []string `json:"hostnames"`
	Tag       string   `json:"tag"`
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
		out[i] = wireRoute{Hostnames: r.Hostnames, Upstream: r.Upstream, RedirectTo: r.RedirectTo, PathPrefix: r.PathPrefix, Traffic: r.Traffic, TunnelOnly: r.TunnelOnly, Stopped: r.Stopped, Hold: r.Hold,
			Wake: r.Wake, QuietProbes: r.QuietProbes}
		if a := r.Access; a != nil {
			out[i].Access = &wireAccess{Username: a.Username, PasswordHash: a.PasswordHash, Key: a.Key, LoginURL: a.LoginURL}
		}
		for _, a := range r.Analytics {
			out[i].Analytics = append(out[i].Analytics, wireAnalyticsHosts(a))
		}
	}
	return out
}

func fromWireRoutes(ws []wireRoute) []node.Route {
	out := make([]node.Route, len(ws))
	for i, w := range ws {
		out[i] = node.Route{Hostnames: w.Hostnames, Upstream: w.Upstream, RedirectTo: w.RedirectTo, PathPrefix: w.PathPrefix, Traffic: w.Traffic, TunnelOnly: w.TunnelOnly, Stopped: w.Stopped, Hold: w.Hold,
			Wake: w.Wake, QuietProbes: w.QuietProbes}
		if a := w.Access; a != nil {
			out[i].Access = &node.Access{Username: a.Username, PasswordHash: a.PasswordHash, Key: a.Key, LoginURL: a.LoginURL}
		}
		for _, a := range w.Analytics {
			out[i].Analytics = append(out[i].Analytics, node.AnalyticsHosts(a))
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

// wireHostTraffic is node.HostTraffic; the duration travels as
// milliseconds. Nodes before v0.13.0 also send a "visitors" sketch, which is
// ignored.
type wireHostTraffic struct {
	Host       string                `json:"host"`
	Requests   uint64                `json:"requests"`
	Status     [4]uint64             `json:"status"`
	Bytes      uint64                `json:"bytes"`
	DurationMS int64                 `json:"duration_ms"`
	Paths      capped[wirePathCount] `json:"paths,omitempty"`
}

type wirePathCount struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Requests uint64 `json:"requests"`
}

func toWireTraffic(hs []node.HostTraffic) []wireHostTraffic {
	out := make([]wireHostTraffic, len(hs))
	for i, h := range hs {
		out[i] = wireHostTraffic{
			Host: h.Host, Requests: h.Requests, Status: h.Status, Bytes: h.Bytes,
			DurationMS: h.Duration.Milliseconds(), Paths: make([]wirePathCount, len(h.Paths)),
		}
		for j, p := range h.Paths {
			out[i].Paths[j] = wirePathCount(p)
		}
	}
	return out
}

func fromWireTraffic(ws []wireHostTraffic) []node.HostTraffic {
	out := make([]node.HostTraffic, len(ws))
	for i, w := range ws {
		out[i] = node.HostTraffic{
			Host: w.Host, Requests: w.Requests, Status: w.Status, Bytes: w.Bytes,
			Duration: time.Duration(w.DurationMS) * time.Millisecond, Paths: make([]node.PathCount, len(w.Paths)),
		}
		for j, p := range w.Paths {
			out[i].Paths[j] = node.PathCount(p)
		}
	}
	return out
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
	// MemoryParts is the memory breakdown; nodes before it send none.
	MemoryParts capped[wireMemoryPart] `json:"memory_parts,omitempty"`
}

type wireMemoryPart struct {
	Group   string `json:"group"`
	Name    string `json:"name"`
	Service string `json:"service,omitempty"`
	Bytes   uint64 `json:"bytes"`
}

func toWireHostStats(h node.HostStats) wireHostStats {
	w := wireHostStats{
		CPUBusy: h.CPUBusy, CPUTotal: h.CPUTotal, CPUs: h.CPUs, MemoryTotal: h.MemoryTotal,
		MemoryAvailable: h.MemoryAvailable, Load1: h.Load1,
	}
	for _, d := range h.Disks {
		w.Disks = append(w.Disks, wireDisk{Paths: d.Paths, Total: d.Total, Used: d.Used})
	}
	for _, p := range h.MemoryParts {
		w.MemoryParts = append(w.MemoryParts, wireMemoryPart{Group: string(p.Group), Name: p.Name, Service: p.Service, Bytes: p.Bytes})
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
	for _, p := range w.MemoryParts {
		h.MemoryParts = append(h.MemoryParts, node.MemoryPart{Group: node.MemoryGroup(p.Group), Name: p.Name, Service: p.Service, Bytes: p.Bytes})
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

func toWireDiskUsage(d node.DiskUsage) wireDiskUsage {
	w := wireDiskUsage{
		ImagesRunning: d.ImagesRunning, ImagesKept: d.ImagesKept, ImagesReclaimable: d.ImagesReclaimable,
		ImagesOther: d.ImagesOther, ImagesShared: d.ImagesShared, OtherVolumes: d.OtherVolumes,
		Containers: d.Containers, DataDir: d.DataDir, Volumes: make(capped[wireVolumeUsage], len(d.Volumes)),
	}
	for i, v := range d.Volumes {
		w.Volumes[i] = wireVolumeUsage(v)
	}
	return w
}

func fromWireDiskUsage(w wireDiskUsage) node.DiskUsage {
	d := node.DiskUsage{
		ImagesRunning: w.ImagesRunning, ImagesKept: w.ImagesKept, ImagesReclaimable: w.ImagesReclaimable,
		ImagesOther: w.ImagesOther, ImagesShared: w.ImagesShared, OtherVolumes: w.OtherVolumes,
		Containers: w.Containers, DataDir: w.DataDir, Volumes: make([]node.VolumeUsage, len(w.Volumes)),
	}
	for i, v := range w.Volumes {
		d.Volumes[i] = node.VolumeUsage(v)
	}
	return d
}

// tunnelRequest is ensure-tunnel's argument: the tunnel cloudflared runs,
// or none (Spec nil) to stop it.
type tunnelRequest struct {
	Spec *wireTunnelSpec `json:"spec,omitempty"`
}

// wireTunnelSpec is node.TunnelSpec on the wire. Token is a secret.
type wireTunnelSpec struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
}

// Analytics (ADR 0037).

// setAnalyticsRequest is node.AnalyticsConfig.
type setAnalyticsRequest struct {
	Salts map[int64][]byte `json:"salts"`
	// Sites null keeps the server's rules; {} clears them.
	Sites map[int64]wireSiteRules `json:"sites"`
	// TraceKey is the trace ingest's key while tracing is on (protocol
	// 25); an older node has no trace ingest and ignores it.
	TraceKey []byte `json:"trace_key,omitempty"`
}

// wireSiteRules is node.SiteRules.
type wireSiteRules struct {
	ExcludedPaths []string `json:"excluded_paths,omitempty"`
	IgnoredIPs    []string `json:"ignored_ips,omitempty"`
	// ErrorsOff is new in A2; an older node ignores it and collects.
	ErrorsOff bool `json:"errors_off,omitempty"`
	// Replay settings, new in protocol 12; an older node records nothing.
	ReplayPercent int  `json:"replay_percent,omitempty"`
	MaskAllText   bool `json:"mask_all_text,omitempty"`
}

type tailSpoolRequest struct {
	After uint64 `json:"after"`
	// Spool is node.Spool; empty is the events' (protocol 12).
	Spool string `json:"spool,omitempty"`
}

// wireSpoolBatch is node.SpoolBatch; the events travel as one string of
// JSON lines.
type wireSpoolBatch struct {
	Position uint64 `json:"position"`
	Events   string `json:"events"`
	Count    int    `json:"count"`
	Skipped  bool   `json:"skipped,omitempty"`
}

func toWireSpoolBatch(b node.SpoolBatch) wireSpoolBatch {
	return wireSpoolBatch{Position: b.Position, Events: string(b.Events), Count: b.Count, Skipped: b.Skipped}
}

func (w wireSpoolBatch) node() node.SpoolBatch {
	return node.SpoolBatch{Position: w.Position, Events: []byte(w.Events), Count: w.Count, Skipped: w.Skipped}
}

type ackSpoolRequest struct {
	Position uint64 `json:"position"`
	Events   int    `json:"events"`
	Spool    string `json:"spool,omitempty"`
}

// analyticsStoreRequest is run-analytics-store's spec. The binary goes by
// its hash: the node runs the copy it received with that hash.
type analyticsStoreRequest struct {
	Spec wireAnalyticsStoreSpec `json:"spec"`
}

type wireAnalyticsStoreSpec struct {
	SHA256      string `json:"sha256"`
	Token       string `json:"token"`
	MemoryBytes int64  `json:"memory_bytes"`
	// Kind is the store, "" for analytics (protocol 24).
	Kind string `json:"kind,omitempty"`
}

// storeKindRequest names the store remove-analytics-store and
// dial-analytics act on, "" for analytics (protocol 24). An older node
// reads it as an empty request, so the client never sends a kind to one.
type storeKindRequest struct {
	Kind string `json:"kind,omitempty"`
}

// putAnalyticsBinaryRequest precedes the binary's bytes.
type putAnalyticsBinaryRequest struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
