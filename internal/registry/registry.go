// Package registry is the control plane's side of the platform registry
// (ADR 0014 section 4 and amendment): CNCF Distribution (`registry:3`) next
// to the control plane, which pushes and pulls only with tokens the control
// plane mints per call.
//
// # Tokens
//
// The registry runs Distribution's token auth with the control plane as
// issuer. Signer mints Ed25519-signed JWTs from a key in the data dir
// (KeyFile, LoadOrCreateKey); the registry verifies them with the public
// key in jwks.json (JWKS). Each token is scoped to one repository and
// lives TokenTTL (PushTokenTTL for a build's push), so no server holds
// registry credentials. Nodes receive tokens in call arguments:
// node.PullOptions.Token for pulls, node.PushTarget.Token for builds.
//
// Docker sends a token it is given straight to the registry. Clients that
// only know the credential flow send it as the password to the realm, the
// token endpoint (Signer.Handler), which hands valid tokens back.
//
// # Container
//
// The registry is infrastructure, never deployed through the platform.
// deploy/install.sh runs it as the dock-registry container (ContainerName),
// listening on :5000 inside and published on the control plane's
// 127.0.0.1:5000 only; nodes reach it through their own registry proxy on
// 127.0.0.1:5000 over the node link, so image refs are
// 127.0.0.1:5000/<repo>@sha256:... on every server. Its config dir,
// /etc/dock/registry, is mounted read-only at /etc/distribution and holds
// config.yml (ConfigYAML) and jwks.json (JWKS). Both are deterministic for
// a given key, Config and Storage, so re-running the installer only
// recreates the registry when something changed. The daemon rewrites
// them too (Platform.UseStorage) when the registry's storage changes.
//
// # Storage
//
// Images are on the control plane's disk ([registry] storage_dir, mounted
// at /var/lib/registry) or in an S3-compatible bucket, under BucketRoot
// (ADR 0034). Platform.Migrate moves them from one to the other.
//
// The config keeps the registry idle when idle: no storage health check
// ticker, no access log, no debug server. Run the container with
// OTEL_TRACES_EXPORTER=none too: Distribution otherwise exports traces to
// localhost:4318 and keeps a 5-second batch timer running.
//
// # Pruning
//
// Client.Prune deletes the manifests no release needs any more through the
// registry API; GarbageCollect then removes the unreferenced blobs with a
// one-shot `registry garbage-collect` container while the registry is
// stopped. The nightly job that calls both lives in core.
package registry

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/NordTempo/tempoDock/internal/s3"
)

const (
	// ContainerName is the registry container on the control plane's server.
	ContainerName = "dock-registry"
	// Image is the Distribution image the registry runs.
	Image = "registry:3.1.2@sha256:be9a7062dd7e3316e5c0368e42740dc991a9ceb5d382317675a8738db0112af0"
	// configPath is the config file inside the container (its config dir
	// is mounted at /etc/distribution).
	configPath = "/etc/distribution/config.yml"
)

// Config names the registry and its token issuer. The Signer that mints
// tokens and the one ConfigYAML renders for must get the same Config, or
// the registry rejects every token.
type Config struct {
	// Service is the registry's name: the audience of its tokens and the
	// service in its auth challenges. Use [registry] public_name.
	Service string
	// Issuer names the control plane in tokens. Empty means "dock".
	Issuer string
	// Realm is the token endpoint's URL (Signer.Handler), where clients
	// without a token are sent.
	Realm string
}

func (c Config) withDefaults() Config {
	if c.Issuer == "" {
		c.Issuer = "dock"
	}
	return c
}

// Storage is where the registry keeps its blobs and manifests (ADR 0034).
type Storage struct {
	// Bucket is an S3-compatible bucket; nil is the filesystem, the
	// storage dir mounted at /var/lib/registry.
	Bucket *s3.Bucket
	// ReadOnly refuses pushes and deletes: Migrate copies the storage
	// while the registry serves pulls from it.
	ReadOnly bool
	// Listen is the registry's address when it runs on the host's network
	// (ADR 0084: [registry] listen, loopback); "" is :5000 in a network
	// namespace of its own, as before, published by Docker.
	Listen string
	// Gateway is the storage gateway's address (ADR 0084, [registry]
	// gateway_listen). With a Bucket and Listen, the registry reaches the
	// bucket through it, signing with GatewayKeys; otherwise it talks to
	// the bucket directly with the bucket's keys (a registry container
	// from before, which can't reach the host's loopback).
	Gateway string
}

// throughGateway: the registry reaches its bucket through the gateway.
func (st Storage) throughGateway() bool {
	return st.Bucket != nil && st.Listen != "" && st.Gateway != ""
}

// GatewayKeys are the access key and secret the registry signs its
// requests to the gateway with: derived from the signing key like
// http.secret, so the config stays deterministic and the bucket's own
// keys stay in the daemon.
func GatewayKeys(s *Signer) (accessKey, secret string) {
	sum := sha256.Sum256(append([]byte("dock registry gateway\x00"), s.key.Seed()...))
	return "dock-registry", hex.EncodeToString(sum[:])
}

// BucketRoot is the registry's directory in a bucket: its objects are
// under BucketRoot/docker/registry/v2/, so a bucket can hold other
// things next to it.
const BucketRoot = "dock-registry"

// ConfigYAML renders Distribution's config.yml for the registry s mints
// tokens for, keeping its images in st. The output only depends on s's
// key, Config and st.
//
// http.secret, which signs upload state between requests, is derived from
// the signing key rather than random, so the output is stable; without
// one the registry makes up a new secret on every start.
//
// In a bucket, redirects stay off: every pull goes through the registry
// (and so through each node's registry proxy, with its per-call tokens)
// instead of to presigned bucket URLs. On the host's network (st.Listen)
// it listens only there and reaches a bucket through the gateway.
func ConfigYAML(s *Signer, st Storage) ([]byte, error) {
	cfg := s.cfg
	if cfg.Service == "" || cfg.Realm == "" {
		return nil, errors.New("registry config: service and realm are required")
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) } // a JSON string is a YAML scalar
	secret := sha256.Sum256(append([]byte("dock registry http.secret\x00"), s.key.Seed()...))
	driver := `
  filesystem:
    rootdirectory: /var/lib/registry`
	switch b := st.Bucket; {
	case st.throughGateway():
		// Path style to the gateway; uploads are never copied in parts
		// (the gateway links a finished upload into place instead).
		access, secret := GatewayKeys(s)
		driver = fmt.Sprintf(`
  s3:
    regionendpoint: %s
    secure: false
    region: %s
    bucket: %s
    forcepathstyle: true
    accesskey: %s
    secretkey: %s
    rootdirectory: /%s
    multipartcopythresholdsize: 5368709120
  redirect:
    disable: true`, q("http://"+st.Gateway), q(s3.DefaultRegion), q(b.Name), q(access), q(secret), BucketRoot)
	case b != nil:
		if err := b.Validate(); err != nil {
			return nil, fmt.Errorf("registry config: %w", err)
		}
		driver = fmt.Sprintf(`
  s3:
    regionendpoint: %s
    secure: %t
    region: %s
    bucket: %s
    forcepathstyle: %t
    accesskey: %s
    secretkey: %s
    rootdirectory: /%s
  redirect:
    disable: true`, q(b.Endpoint), strings.HasPrefix(b.Endpoint, "https:"), q(cmp.Or(b.Region, s3.DefaultRegion)), q(b.Name),
			b.PathStyle, q(b.AccessKeyID), q(b.SecretAccessKey), BucketRoot)
	}
	if st.ReadOnly {
		driver += `
  maintenance:
    readonly:
      enabled: true`
	}
	return []byte(strings.TrimLeft(fmt.Sprintf(`
# Written by dock (internal/registry). Changes are overwritten.
version: 0.1
log:
  level: info
  formatter: text
  accesslog:
    disabled: true
storage:%s
  delete:
    enabled: true
  cache:
    blobdescriptor: inmemory
http:
  addr: %s
  secret: %s
  headers:
    X-Content-Type-Options: [nosniff]
health:
  storagedriver:
    enabled: false
auth:
  token:
    realm: %s
    service: %s
    issuer: %s
    jwks: /etc/distribution/jwks.json
    signingalgorithms: [EdDSA]
`, driver, q(cmp.Or(st.Listen, ":5000")), hex.EncodeToString(secret[:]), q(cfg.Realm), q(cfg.Service), q(cfg.Issuer)), "\n")), nil
}

// WriteConfig writes config.yml (ConfigYAML) and jwks.json (JWKS) for s
// and st into dir, the registry container's /etc/distribution. Files are
// only rewritten when their content changes; changed says whether any
// did, in which case a running registry must be restarted to pick them
// up.
func WriteConfig(dir string, s *Signer, st Storage) (changed bool, err error) {
	yml, err := ConfigYAML(s, st)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	for name, content := range map[string][]byte{"config.yml": yml, "jwks.json": JWKS(s)} {
		c, err := writeIfChanged(filepath.Join(dir, name), content)
		if err != nil {
			return false, fmt.Errorf("registry config: %w", err)
		}
		changed = changed || c
	}
	return changed, nil
}

// writeIfChanged atomically replaces path with content unless it already
// holds exactly that.
func writeIfChanged(path string, content []byte) (bool, error) {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	// config.yml can hold the bucket's keys. install.sh makes dir setgid
	// dock-registry, so the registry process (which doesn't run as root)
	// reads it through its group.
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}
