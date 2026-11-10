package apitypes

import "time"

// Image vulnerability scanning (ADR 0074): every live image is scanned on
// a builder once the platform is quiet, never next to a build or deploy.
// One scan per image, however many releases run it. The service overview
// says when its live image has critical or high findings; the org's
// Images page lists every image in use; each image has a page with its
// findings.

// ScanState is where an image's scan is.
type ScanState string

// Scan states.
const (
	// ScanQueued: waiting for a quiet minute and a builder.
	ScanQueued ScanState = "queued"
	// ScanScanning: a builder is on it.
	ScanScanning ScanState = "scanning"
	// ScanScanned: findings are current as of the scan's database.
	ScanScanned ScanState = "scanned"
	// ScanFailed: it failed five times; Error says why.
	ScanFailed ScanState = "failed"
	// ScanUnsupported: it can't be scanned (Error says why).
	ScanUnsupported ScanState = "unsupported"
)

// FixKind is what fixes a group of findings.
type FixKind string

// Fix kinds.
const (
	// FixRebuild: system packages (apk, deb, rpm) have fixed versions:
	// rebuilding on a newer base image, or upgrading them in the
	// Dockerfile, picks them up.
	FixRebuild FixKind = "rebuild"
	// FixUpgrade: the app's dependencies have fixed versions.
	FixUpgrade FixKind = "upgrade"
	// FixNone: no fixed version is known yet.
	FixNone FixKind = "none"
)

// VulnSeverity is how bad a finding is.
type VulnSeverity string

// Severities, worst first.
const (
	VulnCritical   VulnSeverity = "critical"
	VulnHigh       VulnSeverity = "high"
	VulnMedium     VulnSeverity = "medium"
	VulnLow        VulnSeverity = "low"
	VulnNegligible VulnSeverity = "negligible"
	VulnUnknown    VulnSeverity = "unknown"
)

// VulnCounts counts findings by severity.
type VulnCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	// Other is negligible and unknown.
	Other int `json:"other"`
	// Fixable is how many have a fixed version.
	Fixable int `json:"fixable"`
	// KEV is how many CISA lists as exploited in the wild.
	KEV int `json:"kev"`
}

// ImageScan is the scan of one image.
type ImageScan struct {
	ID    int64     `json:"id"`
	State ScanState `json:"state" enum:"queued,scanning,scanned,failed,unsupported"`
	// Image is how the image is named where it runs: a registry
	// reference, or the platform's own (project/service and release).
	Image  string `json:"image"`
	Digest string `json:"digest,omitempty" doc:"The image's digest or ID, the one thing a scan is kept by"`
	// Built: Dock built the image from source, so a new deploy of the
	// service rebuilds it (and picks up a newer base image). An image
	// deployed by reference is rebuilt where it was built (CI).
	Built bool `json:"built"`
	// Distro is the image's operating system ("alpine 3.20.3").
	Distro   string `json:"distro,omitempty"`
	Packages int    `json:"packages"`
	VulnCounts
	// Truncated counts findings past the 5,000 kept.
	Truncated int `json:"truncated,omitempty"`
	// WorstFix is what fixes the worst findings (critical, else high);
	// empty when there are none.
	WorstFix  FixKind    `json:"worst_fix,omitempty" enum:"rebuild,upgrade,none,"`
	ScannedAt *time.Time `json:"scanned_at,omitempty" doc:"When its findings were last matched"`
	// DatabaseBuilt is the vulnerability database's date the findings
	// are as of.
	DatabaseBuilt *time.Time `json:"database_built,omitempty"`
	// Error says why it failed or can't be scanned, or what it waits for.
	Error string `json:"error,omitempty"`
}

// ImageUse is a release that runs an image.
type ImageUse struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	Release     int64  `json:"release"`
	Production  bool   `json:"production"`
	Live        bool   `json:"live"`
}

// ImageListItem is one image on the Images page: its scan (none yet when
// the release went live only now) and the live releases that run it.
type ImageListItem struct {
	Scan   *ImageScan `json:"scan,omitempty"`
	Image  string     `json:"image"`
	UsedBy []ImageUse `json:"used_by"`
}

// ImageList is the org's images in use, worst first.
type ImageList struct {
	Images []ImageListItem `json:"images"`
	// DatabaseBuilt is the newest vulnerability database a scan used.
	DatabaseBuilt *time.Time `json:"database_built,omitempty"`
}

// Finding is one vulnerability in one package of an image.
type Finding struct {
	ID       string       `json:"id" doc:"CVE-..., GHSA-..."`
	Severity VulnSeverity `json:"severity" enum:"critical,high,medium,low,negligible,unknown"`
	Package  string       `json:"package"`
	Version  string       `json:"version"`
	// Type is the package's kind: apk, deb, rpm (System true), npm,
	// go-module, python, java-archive, ...
	Type    string `json:"type"`
	System  bool   `json:"system" doc:"An operating system package (apk, deb, rpm), from the base image or the Dockerfile's installs"`
	FixedIn string `json:"fixed_in,omitempty"`
	// KEV: CISA lists it as exploited in the wild.
	KEV bool `json:"kev,omitempty"`
	// EPSS is the chance of exploitation in the next 30 days, 0 to 1.
	EPSS      *float64  `json:"epss,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
}

// PackageFix is one package's way out: its version now, the version that
// fixes all of its findings, and those findings' IDs, worst first.
type PackageFix struct {
	Package  string       `json:"package"`
	Version  string       `json:"version"`
	FixedIn  string       `json:"fixed_in,omitempty"`
	Worst    VulnSeverity `json:"worst" enum:"critical,high,medium,low,negligible,unknown"`
	KEV      bool         `json:"kev,omitempty"`
	Findings []string     `json:"findings"`
}

// FixGroup is the findings one kind of fix clears.
type FixGroup struct {
	Kind FixKind `json:"kind" enum:"rebuild,upgrade,none"`
	VulnCounts
	Packages []PackageFix `json:"packages"`
}

// ImageDetail is one image's page: where it runs, its scan, its
// findings grouped by fix and all of them.
type ImageDetail struct {
	Scan     ImageScan  `json:"scan"`
	UsedBy   []ImageUse `json:"used_by"`
	Fixes    []FixGroup `json:"fixes"`
	Findings []Finding  `json:"findings"`
}

// OrgFinding is a finding in a live release, for `dock vulns --all` and
// agents.
type OrgFinding struct {
	Finding
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	Release     int64  `json:"release"`
	Production  bool   `json:"production"`
}

// OrgFindings is what is vulnerable in the org's live releases.
type OrgFindings struct {
	Findings []OrgFinding `json:"findings"`
	// Unscanned names live services whose image has no findings yet,
	// with why.
	Unscanned []string `json:"unscanned,omitempty"`
}

// ScanRequest asks for an image to be scanned again.
type ScanRequest struct {
	Release int64 `json:"release,omitempty" doc:"Release number; default the live one"`
}

// ScanningStatus is scanning on the platform, for operators.
type ScanningStatus struct {
	Enabled bool `json:"enabled"`
	// Images counts scans by state.
	Images map[ScanState]int `json:"images"`
	// Waiting says why nothing is scanned right now, if that is so.
	Waiting  string           `json:"waiting,omitempty"`
	Builders []ScanningServer `json:"builders"`
}

// ScanningServer is one builder's part in scanning.
type ScanningServer struct {
	Server        string     `json:"server"`
	Production    bool       `json:"production" doc:"It accepts production, so it may scan production images"`
	DatabaseBuilt *time.Time `json:"database_built,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// ScanningSettings turns scanning on or off.
type ScanningSettings struct {
	Enabled bool `json:"enabled"`
}
