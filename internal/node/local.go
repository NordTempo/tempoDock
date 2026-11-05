package node

// Local is the in-process Runtime for the control plane's own server, composed
// from the Docker, BuildKit, Caddy, /proc + cgroup (stats), access log
// (traffic), cloudflared (tunnel), analytics (spool and store) and
// uptime probe implementations.
type Local struct {
	Containers
	Builder
	Proxy
	Stats
	Traffic
	Tunnel
	Analytics
	Prober
}
