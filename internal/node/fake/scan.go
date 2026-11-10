package fake

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// fakeScans is the scripted side of node.Scanner: findings per image, the
// database's build time, a gate that holds scans, and the scans seen.
type fakeScans struct {
	mu       sync.Mutex
	findings map[string][]node.Finding
	built    time.Time
	gate     chan struct{}
	err      error
	started  chan string
}

// SetScanFindings makes a scan of any image of repository repo (a
// reference without its tag or digest: "web", "registry/dock/p1_e1_s1")
// find findings.
func (r *Runtime) SetScanFindings(repo string, findings []node.Finding) {
	s := &r.scans
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findings == nil {
		s.findings = map[string][]node.Finding{}
	}
	s.findings[repo] = findings
}

// repoOf is ref without its tag or digest.
func repoOf(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		return ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i]
	}
	return ref
}

// HoldScans makes every SBOM wait until the returned function is called
// (or its ctx ends, which is how a deploy preempts it). started receives
// the image of each SBOM that began waiting.
func (r *Runtime) HoldScans() (release func(), started <-chan string) {
	s := &r.scans
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = make(chan struct{})
	s.started = make(chan string, 16)
	gate := s.gate
	return sync.OnceFunc(func() { close(gate) }), s.started
}

// FailScans makes every SBOM fail with err (nil to succeed again).
func (r *Runtime) FailScans(err error) {
	r.scans.mu.Lock()
	r.scans.err = err
	r.scans.mu.Unlock()
}

// fakeSBOM is the SBOM the fake writes: the image it was made of, so Match
// finds that image's findings, in syft-json's shape.
type fakeSBOM struct {
	Image     string            `json:"dock_fake_image"`
	Artifacts []json.RawMessage `json:"artifacts"`
	Distro    struct {
		ID        string `json:"id"`
		VersionID string `json:"versionID"`
	} `json:"distro"`
}

// SBOM implements node.Scanner, recorded as "scan-sbom <image>". An image
// that isn't on the server is pulled (Pull) when req.Pull is set, and
// removed after.
func (r *Runtime) SBOM(ctx context.Context, req node.SBOMRequest, out io.Writer) (node.SBOMResult, error) {
	r.mu.Lock()
	_, have := r.images[req.Image]
	r.mu.Unlock()
	target, pulled := req.Image, false
	if !have || req.Image == "" {
		if req.Pull == "" {
			return node.SBOMResult{}, fmt.Errorf("image %s: %w on this server", req.Image, node.ErrNotFound)
		}
		if _, err := r.Pull(ctx, req.Pull, io.Discard, req.PullOptions); err != nil {
			return node.SBOMResult{}, err
		}
		target, pulled = req.Pull, true
		defer func() { _ = r.RemoveImage(context.WithoutCancel(ctx), req.Pull) }()
	}
	r.mu.Lock()
	r.op("scan-sbom %s", target)
	r.mu.Unlock()
	s := &r.scans
	s.mu.Lock()
	gate, started, err := s.gate, s.started, s.err
	n := len(s.findings[repoOf(target)])
	if _, scripted := s.findings[repoOf(target)]; !scripted {
		n = len(demoFindings())
	}
	s.mu.Unlock()
	if gate != nil {
		started <- target
		select {
		case <-gate:
		case <-ctx.Done():
			return node.SBOMResult{}, ctx.Err()
		}
	}
	if err != nil {
		return node.SBOMResult{}, err
	}
	doc := fakeSBOM{Image: target, Artifacts: make([]json.RawMessage, n+1)}
	for i := range doc.Artifacts {
		doc.Artifacts[i] = json.RawMessage(fmt.Sprintf(`{"name":"pkg%d"}`, i))
	}
	doc.Distro.ID, doc.Distro.VersionID = "alpine", "3.20.3"
	raw, _ := json.Marshal(doc)
	if _, err := out.Write(raw); err != nil {
		return node.SBOMResult{}, err
	}
	return node.SBOMResult{Distro: "alpine 3.20.3", Packages: n + 1, Bytes: int64(len(raw)), Pulled: pulled}, nil
}

// Match implements node.Scanner, recorded as "scan-match <image>".
func (r *Runtime) Match(ctx context.Context, _ node.MatchRequest, sbom io.Reader, found func(node.Finding) error) (node.MatchResult, error) {
	var doc fakeSBOM
	if err := json.NewDecoder(sbom).Decode(&doc); err != nil {
		return node.MatchResult{}, fmt.Errorf("read the SBOM: %w", err)
	}
	r.mu.Lock()
	r.op("scan-match %s", doc.Image)
	r.mu.Unlock()
	s := &r.scans
	s.mu.Lock()
	built, findings := s.built, s.findings[repoOf(doc.Image)]
	s.mu.Unlock()
	if findings == nil {
		findings = demoFindings()
	}
	if built.IsZero() {
		return node.MatchResult{}, node.ErrNoScanDatabase
	}
	for _, f := range findings {
		if err := ctx.Err(); err != nil {
			return node.MatchResult{}, err
		}
		if err := found(f); err != nil {
			return node.MatchResult{}, err
		}
	}
	return node.MatchResult{DatabaseBuilt: built}, nil
}

// ScanDatabase implements node.Scanner, recorded as "scan-database" with
// " update" when it downloads one (built at the current time).
func (r *Runtime) ScanDatabase(_ context.Context, req node.ScanDatabaseRequest) (node.ScanDatabase, error) {
	r.mu.Lock()
	if req.Update {
		r.op("scan-database update")
	} else {
		r.op("scan-database")
	}
	r.mu.Unlock()
	s := &r.scans
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Update {
		s.built = time.Now().UTC().Truncate(time.Second)
	}
	return node.ScanDatabase{Built: s.built, Schema: "v6"}, nil
}

// ImageDigest implements node.Scanner: an image pulled by a name gets
// <repository>@<its ID> as its repo digest; one that isn't here is not
// found.
func (r *Runtime) ImageDigest(_ context.Context, ref string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.images[ref]
	if !ok {
		return "", fmt.Errorf("image %s: %w", ref, node.ErrNotFound)
	}
	if strings.Contains(ref, "@") {
		return ref, nil
	}
	return repoOf(ref) + "@" + id, nil
}

// demoFindings are the findings in the file DOCK_FAKE_FINDINGS names
// (`make dev` demos, like DOCK_FAKE_LOGS): one JSON node.Finding per line,
// found in every image no test scripted. None without the variable.
func demoFindings() []node.Finding {
	path := os.Getenv("DOCK_FAKE_FINDINGS")
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []node.Finding
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var fd node.Finding
		if json.Unmarshal(sc.Bytes(), &fd) == nil && fd.ID != "" {
			out = append(out, fd)
		}
	}
	return out
}
