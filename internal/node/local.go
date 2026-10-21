package node

// Local is the in-process Runtime for the control plane's own server, composed
// from the Docker, BuildKit and Caddy implementations.
type Local struct {
	Containers
	Builder
	Proxy
}
