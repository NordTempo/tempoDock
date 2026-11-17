package build

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/docker"
)

// checkPushTarget rejects a push target that can't work before anything is
// built.
func checkPushTarget(p *node.PushTarget) error {
	named, err := reference.ParseNormalizedNamed(p.Ref)
	if err != nil {
		return fmt.Errorf("push target %q: %w", p.Ref, err)
	}
	if _, ok := named.(reference.Digested); ok {
		return fmt.Errorf("push target %q must be a tag, not a digest", p.Ref)
	}
	if p.Token == "" {
		return fmt.Errorf("push target %q has no registry token", p.Ref)
	}
	return nil
}

// push pushes the built image (tag, in the local Docker) to p.Ref and
// logs the pushed manifest's digest and how long the push took.
//
// Docker pushes, not BuildKit: the registry is at 127.0.0.1:5000 on every
// server (the node's registry proxy, ADR 0014 amendment), which is the
// host's loopback, out of reach of the BuildKit container on its own
// network. Docker reaches it, uses plain HTTP for loopback registries
// without extra config, and sends the token as is (docker.RegistryAuth).
// The layers stream from Docker's store to the registry; nothing passes
// through the daemon. p.Ref is only a temporary second name for the image.
func (b *Builder) push(ctx context.Context, tag string, p *node.PushTarget, logs io.Writer) error {
	if _, err := b.docker.ImageTag(ctx, client.ImageTagOptions{Source: tag, Target: p.Ref}); err != nil {
		return fmt.Errorf("tag %s: %w", p.Ref, err)
	}
	defer func() {
		_, _ = b.docker.ImageRemove(context.WithoutCancel(ctx), p.Ref, client.ImageRemoveOptions{})
	}()
	_, _ = fmt.Fprintf(logs, "Pushing %s\n", p.Ref)
	started := time.Now()
	resp, err := b.docker.ImagePush(ctx, p.Ref, client.ImagePushOptions{RegistryAuth: docker.RegistryAuth(p.Token)})
	if err != nil {
		return fmt.Errorf("push %s: %w", p.Ref, err)
	}
	defer func() { _ = resp.Close() }()
	var digest string
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("push %s: %w", p.Ref, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("push %s: %s", p.Ref, msg.Error.Message)
		}
		if d, ok := pushedDigest(msg); ok {
			digest = d
		}
	}
	if digest == "" {
		return fmt.Errorf("push %s: docker reported no digest", p.Ref)
	}
	_, _ = fmt.Fprintf(logs, "Pushed %s (%s) in %.1fs\n", p.Ref, digest, time.Since(started).Seconds())
	return nil
}

// pushedDigest reads the pushed manifest's digest from a push progress
// message: the "<tag>: digest: sha256:... size: N" status line, or the
// aux record Docker's classic image store sends with it.
func pushedDigest(msg jsonstream.Message) (string, bool) {
	if msg.Aux != nil {
		var aux struct{ Digest string }
		if json.Unmarshal(*msg.Aux, &aux) == nil && aux.Digest != "" {
			return aux.Digest, true
		}
	}
	if _, rest, ok := strings.Cut(msg.Status, ": digest: "); ok {
		if d, _, _ := strings.Cut(rest, " "); strings.HasPrefix(d, "sha256:") {
			return d, true
		}
	}
	return "", false
}
