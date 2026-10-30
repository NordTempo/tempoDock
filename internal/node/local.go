package node

// Local is the in-process Runtime for the control plane's own server, composed
// from the Docker, BuildKit, Caddy, /proc + cgroup (stats), access log
// (traffic) and cloudflared (tunnel) implementations.
type Local struct {
	Containers
	Builder
	Proxy
	Stats
	Traffic
	Tunnel
}
