package node

// Local is the in-process Runtime for the control plane's own server, composed
// from the Docker, BuildKit, Caddy, /proc + cgroup (stats) and access log
// (traffic) implementations.
type Local struct {
	Containers
	Builder
	Proxy
	Stats
	Traffic
}
