package core

import (
	"context"
)

// Registry is the platform registry as core uses it (ADR 0014 section 4
// and its amendment): CNCF Distribution on the control plane, which every
// server reaches as Host() (127.0.0.1:5000, through the node link on
// remote servers). Core mints a token per pull and push; no server holds
// registry credentials. registry.Platform implements it; nil in
// Options.Registry means there is no registry, and single-server installs
// build and run images locally as before.
type Registry interface {
	// Host is the registry's host:port in image references, the same on
	// every server.
	Host() string
	// Token mints a short-lived token for one repository, for subject
	// (e.g. "server homelab"): push and pull access for a build's push
	// (valid for registry.PushTokenTTL), else pull only.
	Token(subject, repo string, push bool) string
	// Digest returns the manifest digest (sha256:...) repo:tag points at.
	Digest(ctx context.Context, repo, tag string) (string, error)
	// Prune deletes the manifests whose digest isn't in keep and returns
	// how many it deleted.
	Prune(ctx context.Context, keep []string) (int, error)
	// GarbageCollect removes blobs no manifest references any more. The
	// registry is stopped while it runs, so core calls it only when no
	// build or pull uses the registry.
	GarbageCollect(ctx context.Context) error
}
