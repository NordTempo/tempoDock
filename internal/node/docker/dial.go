package docker

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Dial connects from this host to a port of a running service container, on
// the container's IP in its environment network (ADR 0016). Host-originated
// traffic leaves through OUTPUT, which the app firewall leaves alone, and the
// replies are ESTABLISHED, which it lets back in. Containers that aren't
// managed apps (or sidecars) with a service label are not found; no other network or
// address is ever dialed. ctx bounds the inspect calls and connecting only.
func (r *Runtime) Dial(ctx context.Context, req node.DialRequest) (net.Conn, error) {
	if req.Port < 1 || req.Port > 65535 {
		return nil, fmt.Errorf("invalid port %d", req.Port)
	}
	c, err := r.inspectDialable(ctx, req.ContainerID)
	if err != nil {
		return nil, err
	}
	labels := node.LabelsFrom(c.Config.Labels)
	if labels.Service == "" {
		return nil, notFound("service container", req.ContainerID)
	}
	if c.State == nil || c.State.Status != container.StateRunning {
		state := "unknown"
		if c.State != nil {
			state = string(c.State.Status)
		}
		return nil, fmt.Errorf("container %s is %s, not running", req.ContainerID, state)
	}
	addr, err := r.envAddr(ctx, c, labels)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(addr, uint16(req.Port)).String())
	if err != nil {
		return nil, fmt.Errorf("dial %s port %d: %w", req.ContainerID, req.Port, err)
	}
	return conn, nil
}

// envAddr returns c's address on its environment network: one inspect per
// network it is attached to, which is one for containers Start created.
func (r *Runtime) envAddr(ctx context.Context, c container.InspectResponse, labels node.Labels) (netip.Addr, error) {
	var eps []endpoint
	if c.NetworkSettings != nil {
		for _, name := range slices.Sorted(maps.Keys(c.NetworkSettings.Networks)) {
			ep := c.NetworkSettings.Networks[name]
			if ep == nil || ep.NetworkID == "" {
				continue
			}
			res, err := r.cli.NetworkInspect(ctx, ep.NetworkID, client.NetworkInspectOptions{})
			if err != nil {
				continue // removed meanwhile, or not ours to see
			}
			addr := ep.IPAddress
			if !addr.IsValid() {
				addr = ep.GlobalIPv6Address
			}
			eps = append(eps, endpoint{labels: res.Network.Labels, addr: addr})
		}
	}
	addr, ok := envEndpoint(labels, eps)
	if !ok {
		return netip.Addr{}, fmt.Errorf("container %s has no address on its environment network", c.ID)
	}
	return addr, nil
}

// endpoint is a container's attachment to one network.
type endpoint struct {
	labels map[string]string // the network's labels
	addr   netip.Addr
}

// envEndpoint picks the environment network's address among a container's
// endpoints (sorted by network name): a managed network that isn't another
// project's or environment's, preferring one labelled with exactly the
// container's project and environment over one from before networks
// carried those labels. Unmanaged networks (Docker's default bridge, a
// network someone attached by hand) are never used.
func envEndpoint(ct node.Labels, eps []endpoint) (netip.Addr, bool) {
	want := node.Labels{Project: ct.Project, Environment: ct.Environment}
	rank := func(e endpoint) int {
		nl := node.LabelsFrom(e.labels)
		switch {
		case !isManaged(e.labels) || !e.addr.IsValid() || nl.Conflicts(want):
			return 0
		case nl.Project == want.Project && nl.Environment == want.Environment:
			return 2
		default:
			return 1
		}
	}
	best := slices.MaxFunc(append(eps, endpoint{}), func(a, b endpoint) int {
		// MaxFunc keeps the first of equal elements, so ties go to the
		// network that sorts first.
		return cmp.Compare(rank(a), rank(b))
	})
	return best.addr, rank(best) > 0
}

// inspectDialable fetches an app container or a sidecar (a database's
// archiver answers its status this way, ADR 0093).
func (r *Runtime) inspectDialable(ctx context.Context, id string) (container.InspectResponse, error) {
	res, err := r.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return container.InspectResponse{}, notFound("container", id)
	}
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	if res.Container.Config == nil || (!isApp(res.Container.Config.Labels) && !isSidecar(res.Container.Config.Labels)) {
		return container.InspectResponse{}, notFound("container", id)
	}
	return res.Container, nil
}
