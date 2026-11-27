package node

// Local is the in-process Runtime for the control plane's own server, composed
// from the Docker, BuildKit, Caddy, /proc + cgroup (stats), access log
// (traffic), cloudflared (tunnel), analytics (spool and store),
// uptime probe, image scanning (Docker) and host check (hostcheck)
// implementations.
type Local struct {
	Containers
	Builder
	Proxy
	Stats
	Traffic
	Tunnel
	Analytics
	Prober
	Scanner
	HostChecker
}

// Missing names the parts l was built without. A missing part is a nil
// embedded interface: the first call to one of its methods panics, often
// in a background loop (an image scan), which takes the daemon down.
// core.New refuses a Local with any missing.
func (l Local) Missing() []string {
	var out []string
	for _, p := range []struct {
		name string
		nil  bool
	}{
		{"Containers", l.Containers == nil}, {"Builder", l.Builder == nil}, {"Proxy", l.Proxy == nil},
		{"Stats", l.Stats == nil}, {"Traffic", l.Traffic == nil}, {"Tunnel", l.Tunnel == nil},
		{"Analytics", l.Analytics == nil}, {"Prober", l.Prober == nil}, {"Scanner", l.Scanner == nil},
		{"HostChecker", l.HostChecker == nil},
	} {
		if p.nil {
			out = append(out, p.name)
		}
	}
	return out
}
