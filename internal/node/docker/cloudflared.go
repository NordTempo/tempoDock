package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

const (
	// CloudflaredContainer is the container that runs the server's
	// Cloudflare Tunnel connector when the control plane manages the
	// tunnel (ADR 0035).
	CloudflaredContainer = "dock-cloudflared"
	// labelTunnel holds a hash of the image and token the container runs
	// with, so a new token or image recreates it and the same ones don't.
	labelTunnel = "dock.cloudflared.config"
	// cloudflaredMemory caps cloudflared, like the systemd unit install.sh
	// used before (MemoryMax=256M).
	cloudflaredMemory = 256 << 20
)

// Cloudflared is a node.Tunnel: cloudflared in a container on the host's
// network, since Caddy listens on 127.0.0.1 only in tunnel mode and
// trusts forwarding headers from loopback alone.
type Cloudflared struct {
	cli *client.Client
	// name is the container's name, CloudflaredContainer (tests change it).
	name string
	// image is the pinned cloudflared image ([ingress] cloudflared_image).
	image string
	// owner is the data dir of the dock runtime the container belongs to
	// (node.LabelOwner), as for BuildKit: one runtime per Docker host.
	owner string
	// onHostname, when set, is told the tunnel's hostname each time a
	// tunnel is ensured (Caddy's domain checks name it in messages).
	onHostname func(string)
}

var _ node.Tunnel = (*Cloudflared)(nil)

// NewCloudflared returns the tunnel connector of the Docker host r uses.
func NewCloudflared(r *Runtime, image, owner string, onHostname func(string)) *Cloudflared {
	return &Cloudflared{cli: r.cli, name: CloudflaredContainer, image: image, owner: owner, onHostname: onHostname}
}

// EnsureTunnel implements node.Tunnel. The token is passed in the
// container's environment, never its command line; Docker shows the
// environment only to those who can run Docker, root's equals.
func (c *Cloudflared) EnsureTunnel(ctx context.Context, spec *node.TunnelSpec) error {
	inspect, err := c.cli.ContainerInspect(ctx, c.name, client.ContainerInspectOptions{})
	exists := err == nil
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect %s: %w", c.name, err)
	}
	if exists {
		if err := c.checkOwner(inspect.Container.Config.Labels); err != nil {
			return err
		}
	}
	if spec == nil {
		if !exists {
			return nil
		}
		if _, err := c.cli.ContainerRemove(ctx, c.name, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("remove %s: %w", c.name, err)
		}
		return nil
	}
	if c.onHostname != nil {
		c.onHostname(spec.Hostname)
	}
	sum := sha256.Sum256([]byte(c.image + "\x00" + spec.Token))
	want := hex.EncodeToString(sum[:])
	if exists && inspect.Container.Config.Labels[labelTunnel] == want {
		if inspect.Container.State.Running {
			return nil
		}
		return c.start(ctx)
	}
	// Pulled before the old one goes, so a failed pull leaves it running.
	if err := c.pull(ctx); err != nil {
		return err
	}
	if exists {
		if _, err := c.cli.ContainerRemove(ctx, c.name, client.ContainerRemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("remove %s: %w", c.name, err)
		}
	}
	labels := map[string]string{
		node.LabelManaged: "true",
		node.LabelRole:    node.RoleCloudflared,
		labelTunnel:       want,
	}
	if c.owner != "" {
		labels[node.LabelOwner] = c.owner
	}
	_, err = c.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: c.name,
		Config: &container.Config{
			Image: c.image,
			// The image's entrypoint is `cloudflared --no-autoupdate`.
			// Metrics stay on loopback: the default is 0.0.0.0, which the
			// host network would make public. Port 0 picks a free one, so
			// a cloudflared already on the host (install.sh's systemd unit,
			// during adoption) doesn't clash.
			Cmd:    []string{"tunnel", "--metrics", "127.0.0.1:0", "run"},
			Env:    []string{"TUNNEL_TOKEN=" + spec.Token},
			Labels: labels,
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    "host",
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			LogConfig: container.LogConfig{
				Type:   "local",
				Config: map[string]string{"max-size": "10m", "max-file": "3"},
			},
			Resources: container.Resources{Memory: cloudflaredMemory, MemorySwap: NoSwap(cloudflaredMemory)},
		},
	})
	if err != nil {
		return fmt.Errorf("create %s: %w", c.name, err)
	}
	return c.start(ctx)
}

// start starts the container.
func (c *Cloudflared) start(ctx context.Context) error {
	if _, err := c.cli.ContainerStart(ctx, c.name, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start %s: %w", c.name, err)
	}
	return nil
}

// pull fetches the image unless it is there.
func (c *Cloudflared) pull(ctx context.Context) error {
	if _, err := c.cli.ImageInspect(ctx, c.image); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", c.image, err)
	}
	pull, err := c.cli.ImagePull(ctx, c.image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", c.image, err)
	}
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", c.image, err)
	}
	return nil
}

// checkOwner refuses a container named dock-cloudflared that dock didn't
// make, or that another dock runtime on this Docker host made.
func (c *Cloudflared) checkOwner(labels map[string]string) error {
	if labels[node.LabelManaged] != "true" || labels[node.LabelRole] != node.RoleCloudflared {
		return fmt.Errorf("a container named %s exists that dock didn't create; dock runs the Cloudflare tunnel under that name, so rename or remove that container", c.name)
	}
	if owner := labels[node.LabelOwner]; owner != "" && c.owner != "" && owner != c.owner {
		return fmt.Errorf("the container %s on this Docker host belongs to another dock runtime (data dir %s); run one dock runtime per Docker host", c.name, owner)
	}
	return nil
}
