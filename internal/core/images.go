package core

import (
	"net"
	"net/netip"
	"strings"
)

// validateImageRef refuses image references whose registry is this machine
// or a private network (localhost, loopback, link-local, RFC 1918 and ULA
// addresses): a pull makes the Docker daemon connect there, which would let
// a deploy token probe internal services. Docker Hub refs and public
// registries (ghcr.io, registry.example.com:5000) pass. Hostnames are not
// resolved, so a public name pointing at a private address is not caught.
func validateImageRef(ref string) error {
	host := registryHost(ref)
	if host == "" {
		return nil
	}
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return invalid("image %q is on a localhost registry; use a public registry", ref)
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		ip = ip.Unmap()
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return invalid("image %q is on a registry at a private or loopback address (%s); use a public registry", ref, ip)
		}
	}
	return nil
}

// registryHost returns the registry part (host[:port]) of an image
// reference, or "" for Docker Hub. Like Docker, the first path component is
// a registry only if it contains "." or ":" or is "localhost".
func registryHost(ref string) string {
	first, _, ok := strings.Cut(ref, "/")
	if !ok || !strings.ContainsAny(first, ".:") && first != "localhost" {
		return ""
	}
	return first
}
