package node

import (
	"context"
	"errors"
	"io"
	"time"
)

// Scanner makes and matches software bills of materials of images, for
// vulnerability scanning (ADR 0074). Only builders scan, and only while
// the platform is idle: core decides when. Every call runs one job
// container at the lowest CPU weight and cancelling ctx kills it, so a
// build or deploy that arrives meanwhile never waits for a scan.
type Scanner interface {
	// SBOM writes the syft-json SBOM of an image to out (uncompressed),
	// made by Syft running inside a container of the image itself (no
	// network, read-only, the binary mounted from the server). An image
	// that isn't on the server is pulled by req.Pull first and removed
	// again afterwards; one that was there stays.
	SBOM(ctx context.Context, req SBOMRequest, out io.Writer) (SBOMResult, error)
	// Match matches a syft-json SBOM (read from sbom) against the
	// server's vulnerability database with Grype (no network) and calls
	// found for each finding, at most MaxFindings of them, in no particular
	// order. A server without a database answers
	// ErrNoScanDatabase.
	Match(ctx context.Context, req MatchRequest, sbom io.Reader, found func(Finding) error) (MatchResult, error)
	// ScanDatabase reports the server's vulnerability database and, with
	// req.Update, downloads the newest one first (the one call that uses
	// the network).
	ScanDatabase(ctx context.Context, req ScanDatabaseRequest) (ScanDatabase, error)
	// ImageDigest returns the repository digest reference
	// (repo@sha256:...) of a local image pulled as ref, so it can be
	// pulled again exactly elsewhere. An image without one (built or
	// loaded here) answers "".
	ImageDigest(ctx context.Context, ref string) (string, error)
}

// ErrNoScanDatabase is Match on a server without a vulnerability
// database: ScanDatabase with Update gets one.
var ErrNoScanDatabase = errors.New("this server has no vulnerability database yet")

// MaxFindings caps the findings one match reports; the rest are only
// counted (MatchResult.Truncated).
const MaxFindings = 5000

// MaxSBOMBytes caps a syft-json SBOM as Syft writes it.
const MaxSBOMBytes = 64 << 20

// ScanImages are the pinned scanner images (config [scan]).
type ScanImages struct {
	Syft, Grype string
}

// ScanLimits bound one scan job.
type ScanLimits struct {
	MemoryBytes int64
	Timeout     time.Duration
}

// SBOMRequest names the image to make an SBOM of.
type SBOMRequest struct {
	Images ScanImages
	// Image is a local reference or ID tried first (a build tag on the
	// builder that built it).
	Image string
	// Pull, when set, is pulled if Image isn't on the server, with
	// PullOptions, and removed after.
	Pull        string
	PullOptions PullOptions
	Limits      ScanLimits
	// Labels go on the job container (the control plane's own).
	Labels Labels
}

// SBOMResult describes the SBOM written.
type SBOMResult struct {
	// Distro is the image's operating system as Syft names it ("Alpine
	// Linux v3.20"), "" for none.
	Distro   string
	Packages int
	Bytes    int64
	// Pulled is whether the image had to be pulled for the scan.
	Pulled bool
}

// MatchRequest configures a match.
type MatchRequest struct {
	Images ScanImages
	Limits ScanLimits
	Labels Labels
}

// MatchResult sums up a match.
type MatchResult struct {
	// DatabaseBuilt is when the database used was built.
	DatabaseBuilt time.Time
	// Truncated counts findings past MaxFindings that weren't reported.
	Truncated int
}

// Severity of a finding, as Grype grades it.
type Severity int

const (
	SeverityUnknown Severity = iota
	SeverityNegligible
	SeverityLow
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

// Finding is one vulnerability in one package of an image.
type Finding struct {
	// ID is the vulnerability's ID (CVE-..., GHSA-...).
	ID       string
	Severity Severity
	Package  string
	Version  string
	// Type is the package's type as Syft names it: apk, deb, rpm (the
	// system's), npm, go-module, python, java-archive, ... (the app's).
	Type string
	// FixedIn is the first version with a fix, "" when there is none.
	FixedIn string
	// KEV is whether CISA lists it as exploited in the wild.
	KEV bool
	// EPSS is the probability of exploitation in the next 30 days, 0 to
	// 1, or -1 when unknown.
	EPSS float64
}

// ScanDatabaseRequest asks about, or updates, the database.
type ScanDatabaseRequest struct {
	Images ScanImages
	// Update downloads the newest database first. Without it, a server
	// that has none answers with a zero Built.
	Update bool
	Labels Labels
}

// ScanDatabase is a server's vulnerability database.
type ScanDatabase struct {
	// Built is when it was built; zero when the server has none.
	Built  time.Time
	Schema string
}
