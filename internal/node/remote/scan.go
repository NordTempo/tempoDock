package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Image scanning over the link (ADR 0074), protocol 23: scan-sbom streams
// the SBOM down as log frames (it is text), scan-match uploads one and
// gets its findings back as item frames, scan-database and image-digest
// are plain calls. Protocol 27: remote-digest (a plain call) and
// scan-database-import (the archive uploaded as the body). Canceling any of them resets the stream, which kills
// the node's scan job: that is how a deploy preempts a scan.

// ScanProtocol is the first protocol with the scan ops; BaseProtocol
// the first with remote-digest and scan-database-import (phase 3), and
// whose builds report their base image.
const (
	ScanProtocol = 23
	BaseProtocol = 27
)

const (
	opScanSBOM           = "scan-sbom"
	opScanMatch          = "scan-match"
	opScanDatabase       = "scan-database"
	opImageDigest        = "image-digest"
	opRemoteDigest       = "remote-digest"
	opScanDatabaseImport = "scan-database-import"
)

type (
	wireScanImages struct {
		Syft  string `json:"syft"`
		Grype string `json:"grype"`
	}
	wireScanLimits struct {
		MemoryBytes int64 `json:"memory_bytes,omitempty"`
		TimeoutMS   int64 `json:"timeout_ms,omitempty"`
	}
	sbomRequest struct {
		Images   wireScanImages `json:"images"`
		Image    string         `json:"image,omitempty"`
		Pull     string         `json:"pull,omitempty"`
		Token    string         `json:"token,omitempty"`
		Username string         `json:"username,omitempty"`
		Password string         `json:"password,omitempty"`
		Limits   wireScanLimits `json:"limits"`
		Labels   wireLabels     `json:"labels"`
	}
	sbomResult struct {
		Distro   string `json:"distro,omitempty"`
		Packages int    `json:"packages"`
		Bytes    int64  `json:"bytes"`
		Pulled   bool   `json:"pulled,omitempty"`
	}
	matchRequest struct {
		Images wireScanImages `json:"images"`
		Limits wireScanLimits `json:"limits"`
		Labels wireLabels     `json:"labels"`
	}
	matchResult struct {
		DatabaseBuilt time.Time `json:"database_built"`
		Truncated     int       `json:"truncated,omitempty"`
	}
	wireFinding struct {
		ID       string  `json:"id"`
		Severity int     `json:"severity"`
		Package  string  `json:"package"`
		Version  string  `json:"version"`
		Type     string  `json:"type"`
		FixedIn  string  `json:"fixed_in,omitempty"`
		KEV      bool    `json:"kev,omitempty"`
		EPSS     float64 `json:"epss"`
	}
	scanDatabaseRequest struct {
		Images wireScanImages `json:"images"`
		Update bool           `json:"update,omitempty"`
		Labels wireLabels     `json:"labels"`
	}
	scanDatabaseResult struct {
		Built  time.Time `json:"built"`
		Schema string    `json:"schema,omitempty"`
	}
	imageDigestResult struct {
		Digest string `json:"digest"`
	}
	remoteDigestRequest struct {
		Ref      string `json:"ref"`
		Token    string `json:"token,omitempty"`
		Username string `json:"username,omitempty"`
		Password string `json:"password,omitempty"`
	}
	scanImportRequest struct {
		Images wireScanImages `json:"images"`
		Size   int64          `json:"size"`
		Labels wireLabels     `json:"labels"`
	}
)

func toWireScanImages(i node.ScanImages) wireScanImages {
	return wireScanImages{Syft: i.Syft, Grype: i.Grype}
}
func (w wireScanImages) node() node.ScanImages { return node.ScanImages{Syft: w.Syft, Grype: w.Grype} }

func toWireScanLimits(l node.ScanLimits) wireScanLimits {
	return wireScanLimits{MemoryBytes: l.MemoryBytes, TimeoutMS: l.Timeout.Milliseconds()}
}

func (w wireScanLimits) node() node.ScanLimits {
	return node.ScanLimits{MemoryBytes: w.MemoryBytes, Timeout: ms(w.TimeoutMS)}
}

// Scan calls get no deadline of their own: core bounds every scan job
// with its limits and cancels it the moment a build or deploy starts.

// SBOM implements node.Scanner.
func (c *Client) SBOM(ctx context.Context, req node.SBOMRequest, out io.Writer) (node.SBOMResult, error) {
	if err := c.needProtocol(ctx, ScanProtocol, "scan images"); err != nil {
		return node.SBOMResult{}, err
	}
	args := sbomRequest{
		Images: toWireScanImages(req.Images), Image: req.Image, Pull: req.Pull, Token: req.PullOptions.Token,
		Username: req.PullOptions.Username, Password: req.PullOptions.Password,
		Limits: toWireScanLimits(req.Limits), Labels: toWireLabels(req.Labels),
	}
	r, err := callWithLogs[sbomResult](ctx, c, opScanSBOM, 0, args, nil, out)
	return node.SBOMResult(r), err
}

// Match implements node.Scanner.
func (c *Client) Match(ctx context.Context, req node.MatchRequest, sbom io.Reader, found func(node.Finding) error) (node.MatchResult, error) {
	if err := c.needProtocol(ctx, ScanProtocol, "scan images"); err != nil {
		return node.MatchResult{}, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return node.MatchResult{}, err
	}
	args := matchRequest{Images: toWireScanImages(req.Images), Limits: toWireScanLimits(req.Limits), Labels: toWireLabels(req.Labels)}
	var r matchResult
	n := 0
	err = call(ctx, s, opScanMatch, args, sbom, nil, func(raw json.RawMessage) error {
		if n++; n > node.MaxFindings {
			return errTooMany
		}
		var f wireFinding
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		return found(f.node())
	}, &r)
	return node.MatchResult(r), err
}

// ScanDatabase implements node.Scanner.
func (c *Client) ScanDatabase(ctx context.Context, req node.ScanDatabaseRequest) (node.ScanDatabase, error) {
	if err := c.needProtocol(ctx, ScanProtocol, "scan images"); err != nil {
		return node.ScanDatabase{}, err
	}
	r, err := callWithLogs[scanDatabaseResult](ctx, c, opScanDatabase, 0,
		scanDatabaseRequest{Images: toWireScanImages(req.Images), Update: req.Update, Labels: toWireLabels(req.Labels)}, nil, nil)
	return node.ScanDatabase(r), err
}

// ImageDigest implements node.Scanner.
func (c *Client) ImageDigest(ctx context.Context, ref string) (string, error) {
	if err := c.needProtocol(ctx, ScanProtocol, "report image digests"); err != nil {
		return "", err
	}
	r, err := unaryCall[imageDigestResult](ctx, c, opImageDigest, imageRequest{Ref: ref})
	return r.Digest, err
}

// RemoteDigest implements node.Scanner.
func (c *Client) RemoteDigest(ctx context.Context, ref string, opts node.PullOptions) (string, error) {
	if err := c.needProtocol(ctx, BaseProtocol, "check base images"); err != nil {
		return "", err
	}
	r, err := unaryCall[imageDigestResult](ctx, c, opRemoteDigest, remoteDigestRequest{Ref: ref, Token: opts.Token, Username: opts.Username, Password: opts.Password})
	return r.Digest, err
}

// ImportScanDatabase implements node.Scanner: the archive streams up as
// the request's body.
func (c *Client) ImportScanDatabase(ctx context.Context, req node.ScanDatabaseRequest, archive io.Reader, size int64) (node.ScanDatabase, error) {
	if err := c.needProtocol(ctx, BaseProtocol, "import vulnerability databases"); err != nil {
		return node.ScanDatabase{}, err
	}
	r, err := callWithLogs[scanDatabaseResult](ctx, c, opScanDatabaseImport, 0,
		scanImportRequest{Images: toWireScanImages(req.Images), Size: size, Labels: toWireLabels(req.Labels)}, archive, nil)
	return node.ScanDatabase(r), err
}

func (f wireFinding) node() node.Finding {
	return node.Finding{ID: f.ID, Severity: node.Severity(f.Severity), Package: f.Package, Version: f.Version, Type: f.Type, FixedIn: f.FixedIn, KEV: f.KEV, EPSS: f.EPSS}
}

// scanRoutes serves the scan ops (runtimeRoutes).
func scanRoutes(h func(string, http.HandlerFunc), rt node.Scanner) {
	h(opScanSBOM, withLogs(func(ctx context.Context, a sbomRequest, _ io.Reader, logs io.Writer) (sbomResult, error) {
		// Buffered: the responder flushes each write as a frame.
		bw := bufio.NewWriterSize(logs, maxLogChunk)
		r, err := rt.SBOM(ctx, node.SBOMRequest{
			Images: a.Images.node(), Image: a.Image, Pull: a.Pull,
			PullOptions: node.PullOptions{Token: a.Token, Username: a.Username, Password: a.Password},
			Limits:      a.Limits.node(), Labels: a.Labels.node(),
		}, bw)
		if ferr := bw.Flush(); err == nil {
			err = ferr
		}
		return sbomResult(r), err
	}))
	h(opScanMatch, withItems(func(ctx context.Context, a matchRequest, body io.Reader, emit func(any) error) (matchResult, error) {
		r, err := rt.Match(ctx, node.MatchRequest{Images: a.Images.node(), Limits: a.Limits.node(), Labels: a.Labels.node()}, body, func(f node.Finding) error {
			return emit(wireFinding{ID: f.ID, Severity: int(f.Severity), Package: f.Package, Version: f.Version, Type: f.Type, FixedIn: f.FixedIn, KEV: f.KEV, EPSS: f.EPSS})
		})
		return matchResult(r), err
	}))
	h(opScanDatabase, unary(func(ctx context.Context, a scanDatabaseRequest) (scanDatabaseResult, error) {
		r, err := rt.ScanDatabase(ctx, node.ScanDatabaseRequest{Images: a.Images.node(), Update: a.Update, Labels: a.Labels.node()})
		return scanDatabaseResult(r), err
	}))
	h(opImageDigest, unary(func(ctx context.Context, a imageRequest) (imageDigestResult, error) {
		d, err := rt.ImageDigest(ctx, a.Ref)
		return imageDigestResult{Digest: d}, err
	}))
	h(opRemoteDigest, unary(func(ctx context.Context, a remoteDigestRequest) (imageDigestResult, error) {
		d, err := rt.RemoteDigest(ctx, a.Ref, node.PullOptions{Token: a.Token, Username: a.Username, Password: a.Password})
		return imageDigestResult{Digest: d}, err
	}))
	h(opScanDatabaseImport, withLogs(func(ctx context.Context, a scanImportRequest, body io.Reader, _ io.Writer) (scanDatabaseResult, error) {
		r, err := rt.ImportScanDatabase(ctx, node.ScanDatabaseRequest{Images: a.Images.node(), Labels: a.Labels.node()}, body, a.Size)
		return scanDatabaseResult(r), err
	}))
}

// withItems serves a call that may read the rest of the request body and
// sends item frames (emit) before its result.
func withItems[A, R any](fn func(context.Context, A, io.Reader, func(any) error) (R, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := newResponder(w)
		a, body, err := readArgs[A](r)
		if err != nil {
			out.fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		res, err := fn(r.Context(), a, body, func(v any) error {
			raw, err := json.Marshal(v)
			if err != nil {
				return err
			}
			return out.send(frame{Item: raw})
		})
		if err != nil {
			out.fail(wireError(err))
			return
		}
		out.end(res)
	}
}
