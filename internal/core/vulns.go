package core

import (
	"cmp"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/sbom"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Reading image scans (ADR 0074): the org's Images page, one image's
// page, a service's live image, and what is vulnerable across the org.
// Everything takes scope read and shows only images of releases the
// actor can see; a scan shared with another org reveals nothing beyond
// what the reader's own releases run.

// maxOrgFindings bounds `dock vulns --all` and the agents' answer.
const maxOrgFindings = 2000

// ListImages is the org's Images page: every image its live releases
// run, one row per image, worst first. production limits it to
// production environments. Scope: read.
func (c *Core) ListImages(ctx context.Context, a Actor, production bool) (apitypes.ImageList, error) {
	if err := a.require(apitypes.ScopeRead, "reading images"); err != nil {
		return apitypes.ImageList{}, err
	}
	rows, err := c.store.ListOrgImages(ctx, a.OrgID)
	if err != nil {
		return apitypes.ImageList{}, err
	}
	rows = slices.DeleteFunc(rows, func(r store.ListOrgImagesRow) bool {
		return !a.canSee(a.OrgID, r.ProjectID, r.EnvironmentID) || production && r.EnvKind != string(apitypes.KindProduction)
	})
	var ids []int64
	for _, r := range rows {
		if r.ImageScanID != nil && !slices.Contains(ids, *r.ImageScanID) {
			ids = append(ids, *r.ImageScanID)
		}
	}
	scans := map[int64]store.ImageScan{}
	if len(ids) > 0 {
		list, err := c.store.ListImageScansByID(ctx, ids)
		if err != nil {
			return apitypes.ImageList{}, err
		}
		for _, s := range list {
			scans[s.ID] = s
		}
		if err := c.netOrgScans(ctx, a.OrgID, scans, rows); err != nil {
			return apitypes.ImageList{}, err
		}
	}
	waiting := c.scanWaitReason(ctx)
	out := apitypes.ImageList{Images: []apitypes.ImageListItem{}}
	byScan := map[int64]int{}
	for _, r := range rows {
		use := apitypes.ImageUse{Project: r.Project, Environment: r.Environment, Service: r.Service, Release: r.Number,
			Production: r.EnvKind == string(apitypes.KindProduction), Live: true}
		if r.ImageScanID != nil {
			if i, ok := byScan[*r.ImageScanID]; ok {
				out.Images[i].UsedBy = append(out.Images[i].UsedBy, use)
				continue
			}
		}
		name := c.imageName(r.ImageRef, use)
		item := apitypes.ImageListItem{Image: name, UsedBy: []apitypes.ImageUse{use}}
		if r.ImageScanID != nil {
			if s, ok := scans[*r.ImageScanID]; ok {
				scan := toImageScan(s, name)
				if scan.State == apitypes.ScanQueued && scan.Error == "" {
					scan.Error = waiting(s.Production != 0)
				}
				item.Scan = &scan
				if scan.DatabaseBuilt != nil && (out.DatabaseBuilt == nil || scan.DatabaseBuilt.After(*out.DatabaseBuilt)) {
					out.DatabaseBuilt = scan.DatabaseBuilt
				}
			}
			byScan[*r.ImageScanID] = len(out.Images)
		}
		out.Images = append(out.Images, item)
	}
	slices.SortStableFunc(out.Images, func(x, y apitypes.ImageListItem) int {
		return cmp.Or(-cmp.Compare(scanRank(x.Scan), scanRank(y.Scan)), cmp.Compare(x.Image, y.Image))
	})
	return out, nil
}

// netOrgScans leaves out of each image's counts what the org's ignores
// hide wherever it runs live (rows).
func (c *Core) netOrgScans(ctx context.Context, org int64, scans map[int64]store.ImageScan, rows []store.ListOrgImagesRow) error {
	ig, err := c.ignoresOf(ctx, org)
	if err != nil || len(ig) == 0 {
		return err
	}
	uses := map[int64][]vulnUse{}
	for _, r := range rows {
		if r.ImageScanID != nil {
			uses[*r.ImageScanID] = append(uses[*r.ImageScanID], vulnUse{r.ProjectID, r.ServiceID})
		}
	}
	items := make([]netScan, 0, len(scans))
	copies := make([]store.ImageScan, 0, len(scans))
	for _, s := range scans {
		copies = append(copies, s)
	}
	for i := range copies {
		items = append(items, netScan{scan: &copies[i], uses: uses[copies[i].ID]})
	}
	if err := c.netScans(ctx, ig, items); err != nil {
		return err
	}
	for _, s := range copies {
		scans[s.ID] = s
	}
	return nil
}

// netServiceScans leaves out of each of a project's live images (by
// service ID) what the org's ignores hide for that service.
func (c *Core) netServiceScans(ctx context.Context, p store.Project, scanOf map[int64]store.ImageScan) error {
	if len(scanOf) == 0 {
		return nil
	}
	ig, err := c.ignoresOf(ctx, p.OrgID)
	if err != nil || len(ig) == 0 {
		return err
	}
	items := make([]netScan, 0, len(scanOf))
	copies := make([]store.ImageScan, 0, len(scanOf))
	services := make([]int64, 0, len(scanOf))
	for id, s := range scanOf {
		copies, services = append(copies, s), append(services, id)
	}
	for i := range copies {
		items = append(items, netScan{scan: &copies[i], uses: []vulnUse{{p.ID, services[i]}}})
	}
	if err := c.netScans(ctx, ig, items); err != nil {
		return err
	}
	for i, id := range services {
		scanOf[id] = copies[i]
	}
	return nil
}

// scanRank orders images worst first: by criticals, then highs, then
// the rest; scanned before not scanned yet.
func scanRank(s *apitypes.ImageScan) int64 {
	if s == nil || s.State != apitypes.ScanScanned {
		return -1
	}
	return int64(s.Critical)<<40 | int64(min(s.High, 1<<20-1))<<20 | int64(min(s.Medium+s.Low+s.Other, 1<<20-1))
}

// scanWaitReason returns what queued scans wait for, given whether one
// needs a builder that builds production: one server list read once.
func (c *Core) scanWaitReason(ctx context.Context) func(production bool) string {
	c.scans.mu.Lock()
	enabled := c.scans.enabled
	c.scans.mu.Unlock()
	if !enabled {
		return func(bool) string { return "Scanning is off: an operator turns it on with `dock scanning on`" }
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return func(bool) string { return "" }
	}
	return c.scanWaitFor(servers)
}

// scanWaitFor is scanWaitReason over servers already read: what a queued
// scan of a production or non-production image waits for.
func (c *Core) scanWaitFor(servers []store.Server) func(production bool) string {
	any, prod, nonProd := false, false, false
	for _, s := range servers {
		if c.canBuild(s) {
			any, prod, nonProd = true, prod || s.BuildsProduction, nonProd || s.BuildsNonProduction
		}
	}
	onlineProd, onlineNonProd := c.scanKinds(servers)
	return func(production bool) string {
		switch {
		case !any:
			return "No server builds images, and only builders scan; make one a builder with `dock server update NAME --builder`"
		case production && !prod:
			return "Production images are scanned only on builders that build production, and none does; let one with `dock server update NAME --builds all`"
		case !production && !nonProd:
			return "Non-production images are scanned only on builders that build non-production, and none does; let one with `dock server update NAME --builds all`"
		case production && !onlineProd, !production && !onlineNonProd:
			return "Waiting for a builder that scans these images to come back online, or for its disk to have room"
		}
		return "Waiting for a quiet minute: builds and deploys come first"
	}
}

// imageName is how an image is named on the Images page: a registry
// reference as written, or for the platform's own builds the service
// and release that built it ("shop/web r42").
func (c *Core) imageName(ref string, use apitypes.ImageUse) string {
	if c.dockNamed(ref) {
		return fmt.Sprintf("%s/%s r%d", use.Project, use.Service, use.Release)
	}
	return ref
}

// GetImage is one image's page: its scan, the org's releases that run
// it, its findings grouped by fix and all of them. An image none of the
// actor's releases runs is not found. Scope: read.
func (c *Core) GetImage(ctx context.Context, a Actor, scanID int64) (apitypes.ImageDetail, error) {
	if err := a.require(apitypes.ScopeRead, "reading images"); err != nil {
		return apitypes.ImageDetail{}, err
	}
	return c.imageDetail(ctx, a, scanID, "", nil)
}

// imageDetail builds an image's page for a; name, when set, is how the
// image is named. Ignores hide findings that are ignored at every one of
// at, or with none, everywhere the org runs it live (else everywhere it
// ran).
func (c *Core) imageDetail(ctx context.Context, a Actor, scanID int64, name string, at []vulnUse) (apitypes.ImageDetail, error) {
	rels, err := c.store.ListImageScanReleases(ctx, store.ListImageScanReleasesParams{ScanID: &scanID, OrgID: a.OrgID})
	if err != nil {
		return apitypes.ImageDetail{}, err
	}
	rels = slices.DeleteFunc(rels, func(r store.ListImageScanReleasesRow) bool { return !a.canSee(a.OrgID, r.ProjectID, r.EnvironmentID) })
	if len(rels) == 0 {
		return apitypes.ImageDetail{}, notFound("image %d not found; `dock images` lists the images your releases run", scanID)
	}
	row, err := c.store.GetImageScan(ctx, scanID)
	if err != nil {
		return apitypes.ImageDetail{}, err
	}
	findings, err := c.store.ListImageFindings(ctx, store.ListImageFindingsParams{ScanID: scanID, MaxRows: node.MaxFindings})
	if err != nil {
		return apitypes.ImageDetail{}, err
	}
	ignores, err := c.store.ListVulnIgnores(ctx, store.ListVulnIgnoresParams{OrgID: a.OrgID, Cutoff: ptr(now().Unix())})
	if err != nil {
		return apitypes.ImageDetail{}, err
	}
	out := apitypes.ImageDetail{UsedBy: make([]apitypes.ImageUse, 0, len(rels)), Findings: make([]apitypes.Finding, 0, len(findings)), Ignores: []apitypes.VulnIgnore{}}
	var live, ran []vulnUse
	for _, r := range rels {
		u := apitypes.ImageUse{Project: r.Project, Environment: r.Environment, Service: r.Service, Release: r.Number,
			Production: r.EnvKind == string(apitypes.KindProduction), Live: r.Status == string(apitypes.ReleaseActive)}
		out.UsedBy = append(out.UsedBy, u)
		vu := vulnUse{r.ProjectID, r.ServiceID}
		if u.Live && !slices.Contains(live, vu) {
			live = append(live, vu)
		}
		if !slices.Contains(ran, vu) {
			ran = append(ran, vu)
		}
	}
	if at == nil {
		at = live
		if len(at) == 0 {
			at = ran
		}
	}
	if name == "" {
		name = c.imageName(rels[0].ImageRef, out.UsedBy[0])
	}
	var ig ignoreSet
	for _, g := range ignores {
		if g.ExpiresAt == nil || *g.ExpiresAt > now().Unix() {
			ig = append(ig, store.VulnIgnore{ID: g.ID, ProjectID: g.ProjectID, ServiceID: g.ServiceID, VulnID: g.VulnID, Package: g.Package})
		}
	}
	shows := map[int64]bool{} // ignores that hide a finding somewhere it runs
	var counted []apitypes.Finding
	var worst []node.Finding
	for _, f := range findings {
		fd := toFinding(f.Severity, f.VulnID, f.Package, f.Version, f.PkgType, f.FixedIn, f.Kev, f.Epss, f.FirstSeenAt)
		for _, g := range ig {
			if !shows[g.ID] && slices.ContainsFunc(ran, func(u vulnUse) bool { return covers(g, f.VulnID, f.Package, u) }) {
				shows[g.ID] = true
			}
		}
		if ig.hidden(f.VulnID, f.Package, at) {
			fd.Ignored = true
			uncount(&row, f.Severity, f.FixedIn != "", f.Kev != 0)
		} else {
			counted = append(counted, fd)
			if f.Severity >= int64(node.SeverityHigh) {
				worst = append(worst, node.Finding{Severity: node.Severity(f.Severity), Type: f.PkgType, FixedIn: f.FixedIn})
			}
		}
		out.Findings = append(out.Findings, fd)
	}
	if len(counted) < len(out.Findings) {
		row.WorstFix = string(countFindings(worst).worstFix)
	}
	for _, g := range ignores {
		if shows[g.ID] {
			out.Ignores = append(out.Ignores, toVulnIgnoreRow(g))
		}
	}
	out.Scan = toImageScan(row, name)
	if out.Scan.State == apitypes.ScanQueued && out.Scan.Error == "" {
		out.Scan.Error = c.scanWaitReason(ctx)(row.Production != 0)
	}
	out.Fixes = fixGroups(counted)
	return out, nil
}

// ServiceVulnerabilities is the image of a service's live release (or of
// release N), as its page shows it. Scope: read.
func (c *Core) ServiceVulnerabilities(ctx context.Context, a Actor, ref ServiceRef, release int64) (apitypes.ImageDetail, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading vulnerabilities")
	if err != nil {
		return apitypes.ImageDetail{}, err
	}
	r, err := c.scanRelease(ctx, ss, release)
	if err != nil {
		return apitypes.ImageDetail{}, err
	}
	if r.ImageScanID == nil {
		if r.Status == string(apitypes.ReleaseActive) {
			return apitypes.ImageDetail{}, notFound("release %d of %s isn't scanned yet: scans start once no build or deploy has run for a minute", r.Number, ss)
		}
		return apitypes.ImageDetail{}, notFound("release %d of %s has no scan; scan it with `dock scan %s -r %d`", r.Number, ss, ss.svc.Name, r.Number)
	}
	use := apitypes.ImageUse{Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name, Release: r.Number}
	return c.imageDetail(ctx, a, *r.ImageScanID, c.imageName(r.ImageRef, use), []vulnUse{{ss.project.ID, ss.svc.ID}})
}

// scanRelease is release N of a service, or its live one for 0.
func (c *Core) scanRelease(ctx context.Context, ss svcScope, number int64) (store.Release, error) {
	if number > 0 {
		return c.releaseByNumber(ctx, ss, number)
	}
	r, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return r, notFound("%s has no live release; name one with -r N", ss)
	}
	return r, err
}

// OrgFindings is what is vulnerable in the org's live releases, at
// least min severity, worst first: `dock vulns --all` and agents' "what
// is vulnerable in production". Scope: read.
func (c *Core) OrgFindings(ctx context.Context, a Actor, production bool, minSeverity apitypes.VulnSeverity) (apitypes.OrgFindings, error) {
	if err := a.require(apitypes.ScopeRead, "reading vulnerabilities"); err != nil {
		return apitypes.OrgFindings{}, err
	}
	rows, err := c.store.ListOrgFindings(ctx, store.ListOrgFindingsParams{OrgID: a.OrgID, MinSeverity: int64(severityLevel(minSeverity)), MaxRows: maxOrgFindings})
	if err != nil {
		return apitypes.OrgFindings{}, err
	}
	ig, err := c.ignoresOf(ctx, a.OrgID)
	if err != nil {
		return apitypes.OrgFindings{}, err
	}
	out := apitypes.OrgFindings{Findings: []apitypes.OrgFinding{}}
	for _, r := range rows {
		prod := r.EnvKind == string(apitypes.KindProduction)
		if !a.canSee(a.OrgID, r.ProjectID, r.EnvironmentID) || production && !prod || ig.hides(r.VulnID, r.Package, vulnUse{r.ProjectID, r.ServiceID}) {
			continue
		}
		out.Findings = append(out.Findings, apitypes.OrgFinding{
			Finding: toFinding(r.Severity, r.VulnID, r.Package, r.Version, r.PkgType, r.FixedIn, r.Kev, r.Epss, 0),
			Project: r.Project, Environment: r.Environment, Service: r.Service, Release: r.Number, Production: prod,
		})
	}
	images, err := c.ListImages(ctx, a, production)
	if err != nil {
		return out, err
	}
	for _, img := range images.Images {
		if img.Scan != nil && img.Scan.State == apitypes.ScanScanned {
			continue
		}
		why := "not scanned yet"
		if img.Scan != nil && img.Scan.Error != "" {
			why = img.Scan.Error
		}
		for _, u := range img.UsedBy {
			out.Unscanned = append(out.Unscanned, fmt.Sprintf("%s/%s/%s: %s", u.Project, u.Environment, u.Service, why))
		}
	}
	return out, nil
}

// ScanService queues the image of a service's live release (or release
// N) for a scan: again from its stored SBOM when it has one, which only
// matches it against the current database. It still waits for a quiet
// minute. Scope: deploy on the environment.
func (c *Core) ScanService(ctx context.Context, a Actor, ref ServiceRef, release int64) (apitypes.ImageScan, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "scanning images")
	if err != nil {
		return apitypes.ImageScan{}, err
	}
	r, err := c.scanRelease(ctx, ss, release)
	if err != nil {
		return apitypes.ImageScan{}, err
	}
	use := apitypes.ImageUse{Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name, Release: r.Number}
	if r.ImageScanID == nil {
		if err := c.addReleaseScan(ctx, ss, r); err != nil {
			return apitypes.ImageScan{}, err
		}
		if r, err = c.store.GetRelease(ctx, r.ID); err != nil {
			return apitypes.ImageScan{}, err
		}
	}
	if r.ImageScanID == nil {
		// A live release without a pinned image yet: the scanner's next
		// pass attaches it.
		c.kickScans()
		return apitypes.ImageScan{State: apitypes.ScanQueued, Image: c.imageName(r.ImageRef, use)}, nil
	}
	if err := c.store.RequeueImageScan(ctx, store.RequeueImageScanParams{Priority: 2, ID: *r.ImageScanID}); err != nil {
		return apitypes.ImageScan{}, err
	}
	c.audit(ctx, a, "service.scan", &ss.project, &ss.env, ss.String(), fmt.Sprintf("queued a scan of release %d's image", r.Number))
	c.kickScans()
	row, err := c.store.GetImageScan(ctx, *r.ImageScanID)
	if err != nil {
		return apitypes.ImageScan{}, err
	}
	c.publishScan(ctx, row)
	return toImageScan(row, c.imageName(r.ImageRef, use)), nil
}

// addReleaseScan gives a release that isn't live its image's scan, asked
// for by hand. A live one is left to the scanner, which pins CI images
// on their server while the platform is quiet.
func (c *Core) addReleaseScan(ctx context.Context, ss svcScope, r store.Release) error {
	if r.Status == string(apitypes.ReleaseActive) {
		return nil
	}
	// An image Dock built without a registry is read from the server by
	// its name: only for the release's own project, and only once the
	// release got as far as having one.
	if r.ImageDigest == "" && r.PinnedRef == "" && (!c.ownImage(ss.project.ID, r.ImageRef) || r.ImageID == "") {
		return invalid("release %d's image can only be scanned while it is live, since Dock didn't record its digest; roll back to it, or scan the live release", r.Number)
	}
	img, _ := c.scanImageOf(ctx, store.ListUnscannedReleasesRow{
		ID: r.ID, ServiceID: r.ServiceID, ImageRef: r.ImageRef, ImageID: r.ImageID, ImageDigest: r.ImageDigest,
		PinnedRef: r.PinnedRef, BuiltOnServerID: r.BuiltOnServerID, EnvKind: ss.env.Kind, ServerID: ss.env.ServerID, OrgID: ss.project.OrgID,
	})
	return c.store.Tx(ctx, func(q *store.Queries) error {
		scan, err := q.GetImageScanByKey(ctx, img.key)
		if store.IsNotFound(err) {
			scan, err = q.AddImageScan(ctx, store.AddImageScanParams{
				ImageKey: img.key, PullRef: img.pull, LocalRef: img.local, BuiltOnServerID: img.builtOn, OrgID: ss.project.OrgID,
				Production: boolInt(ss.env.Kind == string(apitypes.KindProduction)), Priority: 2,
				BaseImage: r.BaseImage, BaseDigest: r.BaseDigest, CreatedAt: now().Unix(),
			})
		}
		if err != nil {
			return err
		}
		return q.SetReleaseImageScan(ctx, store.SetReleaseImageScanParams{ImageScanID: &scan.ID, ID: r.ID})
	})
}

// ScanningStatus is scanning on the platform, for operators.
func (c *Core) ScanningStatus(ctx context.Context, a Actor) (apitypes.ScanningStatus, error) {
	if err := a.requirePlatform(PlatformView, "reading the scanning status"); err != nil {
		return apitypes.ScanningStatus{}, err
	}
	counts, err := c.store.CountImageScans(ctx)
	if err != nil {
		return apitypes.ScanningStatus{}, err
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return apitypes.ScanningStatus{}, err
	}
	c.scans.mu.Lock()
	out := apitypes.ScanningStatus{Enabled: c.scans.enabled, Offline: c.cfg.Scan.Offline, Imported: c.scans.imported,
		Images: map[apitypes.ScanState]int{}, Builders: []apitypes.ScanningServer{}}
	busy := c.scans.busy > 0 || time.Since(c.scans.lastBusy) < c.quietFor()
	c.scans.mu.Unlock()
	queuedProd, queuedNonProd := false, false
	for _, n := range counts {
		out.Images[apitypes.ScanState(n.State)] += int(n.N)
		if n.State == string(apitypes.ScanQueued) && n.N > 0 {
			queuedProd, queuedNonProd = queuedProd || n.Production != 0, queuedNonProd || n.Production == 0
		}
	}
	for _, s := range servers {
		if !c.canBuild(s) {
			continue
		}
		b := apitypes.ScanningServer{Server: s.Name, Production: s.BuildsProduction, Error: s.ScanError,
			Imported: out.Imported != "" && s.ScanDbImport == out.Imported}
		if s.ScanDbBuiltAt != nil {
			b.DatabaseBuilt = ptr(time.Unix(*s.ScanDbBuiltAt, 0).UTC())
		}
		out.Builders = append(out.Builders, b)
	}
	prod, nonProd := c.scanKinds(servers)
	switch {
	case !out.Enabled:
	case len(out.Builders) == 0:
		out.Waiting = "No server builds images, and only builders scan"
	case queuedProd && !prod:
		out.Waiting = c.scanWaitFor(servers)(true)
	case queuedNonProd && !nonProd:
		out.Waiting = c.scanWaitFor(servers)(false)
	case out.Images[apitypes.ScanQueued] > 0 && busy:
		out.Waiting = "Waiting for a quiet minute: builds and deploys come first"
	}
	return out, nil
}

// SetScanning turns scanning on or off for the platform. Off stops a
// running scan at once; findings already made stay, and with
// RemoveDatabase every builder's vulnerability database goes too (ADR
// 0101); on downloads it again. Operators only.
func (c *Core) SetScanning(ctx context.Context, a Actor, req apitypes.ScanningSettings) (apitypes.ScanningStatus, error) {
	on := req.Enabled
	if err := a.requirePlatform(PlatformSettings, "turning scanning on or off"); err != nil {
		return apitypes.ScanningStatus{}, err
	}
	if err := c.store.SetScanning(ctx, store.SetScanningParams{Scanning: boolInt(on), UpdatedBy: a.String(), UpdatedAt: now()}); err != nil {
		return apitypes.ScanningStatus{}, err
	}
	c.scans.mu.Lock()
	c.scans.enabled = on
	if !on && c.scans.cancel != nil {
		c.scans.cancel()
	}
	c.scans.mu.Unlock()
	word, summary := "off", "turned image scanning off"
	var removeErr error
	switch {
	case on:
		word, summary = "on", "turned image scanning on"
		c.kickScans()
	case req.RemoveDatabase:
		removeErr = c.removeScanDatabases(ctx)
		summary += " and removed the builders' vulnerability databases"
		if removeErr != nil {
			summary += "; some stayed: " + removeErr.Error()
		}
	}
	c.audit(ctx, a.platform(), "scanning."+word, nil, nil, "scanning", summary)
	if removeErr != nil {
		return apitypes.ScanningStatus{}, fmt.Errorf("scanning is off, but %w", removeErr)
	}
	return c.ScanningStatus(ctx, a)
}

// toImageScan is a scan as the API shows it, named name.
func toImageScan(s store.ImageScan, name string) apitypes.ImageScan {
	out := apitypes.ImageScan{
		ID: s.ID, State: apitypes.ScanState(s.State), Image: name, Built: s.LocalRef != "", Digest: strings.TrimPrefix(s.ImageKey, "local:"),
		Distro: s.Distro, Packages: int(s.Packages), Truncated: int(s.Truncated), WorstFix: apitypes.FixKind(s.WorstFix), Error: s.Error,
		VulnCounts: apitypes.VulnCounts{Critical: int(s.Critical), High: int(s.High), Medium: int(s.Medium), Low: int(s.Low), Other: int(s.Other), Fixable: int(s.Fixable), KEV: int(s.Kev)},
	}
	if strings.HasPrefix(s.ImageKey, "release:") {
		out.Digest = ""
	}
	if s.MatchedAt != nil {
		out.ScannedAt = ptr(time.Unix(*s.MatchedAt, 0).UTC())
	}
	if s.DbBuiltAt != nil {
		out.DatabaseBuilt = ptr(time.Unix(*s.DbBuiltAt, 0).UTC())
	}
	if s.BaseImage != "" {
		out.Base = &apitypes.ImageBase{Image: s.BaseImage, Digest: s.BaseDigest, Moved: s.BaseMoved != 0, Latest: s.BaseLatest, Own: s.BaseDigest == s.ImageKey}
		if s.BaseCheckedAt != nil {
			out.Base.CheckedAt = ptr(time.Unix(*s.BaseCheckedAt, 0).UTC())
		}
	}
	return out
}

// toFinding is a stored finding as the API shows it.
func toFinding(sev int64, id, pkg, version, typ, fixed string, kev int64, epss float64, firstSeen int64) apitypes.Finding {
	f := apitypes.Finding{
		ID: id, Severity: severityName(node.Severity(sev)), Package: pkg, Version: version, Type: typ,
		System: systemPackage(typ), FixedIn: fixed, KEV: kev != 0,
	}
	if epss >= 0 {
		f.EPSS = &epss
	}
	if firstSeen > 0 {
		f.FirstSeen = time.Unix(firstSeen, 0).UTC()
	}
	return f
}

// severityName and severityLevel convert between the node's grades and
// the API's words.
func severityName(s node.Severity) apitypes.VulnSeverity {
	switch s {
	case node.SeverityCritical:
		return apitypes.VulnCritical
	case node.SeverityHigh:
		return apitypes.VulnHigh
	case node.SeverityMedium:
		return apitypes.VulnMedium
	case node.SeverityLow:
		return apitypes.VulnLow
	case node.SeverityNegligible:
		return apitypes.VulnNegligible
	}
	return apitypes.VulnUnknown
}

func severityLevel(s apitypes.VulnSeverity) node.Severity {
	for l := node.SeverityCritical; l > node.SeverityUnknown; l-- {
		if severityName(l) == s {
			return l
		}
	}
	return node.SeverityUnknown
}

// fixGroups groups findings by what fixes them (rebuild, upgrade, none),
// then by package: worst groups first, "none" last.
func fixGroups(findings []apitypes.Finding) []apitypes.FixGroup {
	groups := map[apitypes.FixKind]*apitypes.FixGroup{}
	pkgs := map[apitypes.FixKind]map[[2]string]int{}
	for _, f := range findings {
		kind := apitypes.FixNone
		switch {
		case f.FixedIn == "":
		case f.System:
			kind = apitypes.FixRebuild
		default:
			kind = apitypes.FixUpgrade
		}
		g := groups[kind]
		if g == nil {
			g = &apitypes.FixGroup{Kind: kind, Packages: []apitypes.PackageFix{}}
			groups[kind], pkgs[kind] = g, map[[2]string]int{}
		}
		addCount(&g.VulnCounts, f)
		key := [2]string{f.Package, f.Version}
		i, ok := pkgs[kind][key]
		if !ok {
			i = len(g.Packages)
			pkgs[kind][key] = i
			g.Packages = append(g.Packages, apitypes.PackageFix{Package: f.Package, Version: f.Version, Worst: f.Severity, Findings: []string{}})
		}
		p := &g.Packages[i]
		p.Findings = append(p.Findings, f.ID)
		p.KEV = p.KEV || f.KEV
		if severityLevel(f.Severity) > severityLevel(p.Worst) {
			p.Worst = f.Severity
		}
		if f.FixedIn != "" && (p.FixedIn == "" || versionLess(p.FixedIn, f.FixedIn)) {
			p.FixedIn = f.FixedIn
		}
	}
	out := make([]apitypes.FixGroup, 0, len(groups))
	for _, g := range groups {
		slices.SortStableFunc(g.Packages, func(x, y apitypes.PackageFix) int {
			return cmp.Or(-cmp.Compare(severityLevel(x.Worst), severityLevel(y.Worst)), -cmp.Compare(len(x.Findings), len(y.Findings)), cmp.Compare(x.Package, y.Package))
		})
		out = append(out, *g)
	}
	slices.SortFunc(out, func(x, y apitypes.FixGroup) int {
		if (x.Kind == apitypes.FixNone) != (y.Kind == apitypes.FixNone) {
			return boolCmp(x.Kind == apitypes.FixNone)
		}
		return cmp.Or(-cmp.Compare(x.Critical, y.Critical), -cmp.Compare(x.High, y.High), -cmp.Compare(x.Medium, y.Medium), cmp.Compare(x.Kind, y.Kind))
	})
	return out
}

// boolCmp orders false before true.
func boolCmp(b bool) int {
	if b {
		return 1
	}
	return -1
}

// addCount counts one finding.
func addCount(n *apitypes.VulnCounts, f apitypes.Finding) {
	switch f.Severity {
	case apitypes.VulnCritical:
		n.Critical++
	case apitypes.VulnHigh:
		n.High++
	case apitypes.VulnMedium:
		n.Medium++
	case apitypes.VulnLow:
		n.Low++
	default:
		n.Other++
	}
	if f.FixedIn != "" {
		n.Fixable++
	}
	if f.KEV {
		n.KEV++
	}
}

// versionLess compares versions the way people read them: runs of
// digits as numbers, everything else as text ("1.36.1-r29" < "1.36.1-r31",
// "0.9" < "0.12"). Close enough across ecosystems to pick the version
// that fixes all of a package's findings.
func versionLess(a, b string) bool {
	for a != "" && b != "" {
		x, ra := versionPart(a)
		y, rb := versionPart(b)
		if x != y {
			xd, yd := isDigits(x), isDigits(y)
			if xd && yd {
				x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
				if len(x) != len(y) {
					return len(x) < len(y)
				}
			}
			return x < y
		}
		a, b = ra, rb
	}
	return len(a) < len(b)
}

// versionPart splits off the leading run of digits or of non-digits.
func versionPart(s string) (string, string) {
	digit := unicode.IsDigit(rune(s[0]))
	i := 1
	for i < len(s) && unicode.IsDigit(rune(s[i])) == digit {
		i++
	}
	return s[:i], s[i:]
}

func isDigits(s string) bool { return s != "" && unicode.IsDigit(rune(s[0])) }

// SBOMExport is a service's SBOM ready to send: its file name, and
// Write, which streams it gzipped in the format asked for.
type SBOMExport struct {
	Name  string
	Write func(w io.Writer) error
}

// sbomNames are the file name endings of each format.
var sbomNames = map[sbom.Format]string{sbom.SyftJSON: "sbom.json.gz", sbom.CycloneDXJSON: "cdx.json.gz", sbom.SPDXJSON: "spdx.json.gz"}

// ExportSBOM opens the stored SBOM of a service's live image (or release
// N's) in format: Syft's JSON as stored, or converted as it streams to
// CycloneDX 1.6 or SPDX 2.3 JSON (one package in memory at a time, on the
// control plane: no job, so nothing runs beside a deploy). Always
// gzipped. Scope: read.
func (c *Core) ExportSBOM(ctx context.Context, a Actor, ref ServiceRef, release int64, format sbom.Format) (SBOMExport, error) {
	if format == "" {
		format = sbom.SyftJSON
	}
	if !sbom.Valid(format) {
		return SBOMExport{}, invalid("format %q isn't one of syft-json, cyclonedx-json, spdx-json", format)
	}
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading SBOMs")
	if err != nil {
		return SBOMExport{}, err
	}
	r, err := c.scanRelease(ctx, ss, release)
	if err != nil {
		return SBOMExport{}, err
	}
	missing := notFound("release %d of %s has no SBOM yet: it is made when its image is scanned", r.Number, ss)
	if r.ImageScanID == nil {
		return SBOMExport{}, missing
	}
	f, err := os.Open(c.sbomPath(*r.ImageScanID))
	if err != nil {
		return SBOMExport{}, missing
	}
	name := fmt.Sprintf("%s-r%d.%s", ss.svc.Name, r.Number, sbomNames[format])
	if format == sbom.SyftJSON {
		return SBOMExport{Name: name, Write: func(w io.Writer) error {
			defer func() { _ = f.Close() }()
			_, err := io.Copy(w, f)
			return err
		}}, nil
	}
	scan, err := c.store.GetImageScan(ctx, *r.ImageScanID)
	if err != nil {
		_ = f.Close()
		return SBOMExport{}, err
	}
	use := apitypes.ImageUse{Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name, Release: r.Number}
	img := sbom.Image{Name: c.imageName(r.ImageRef, use), Distro: scan.Distro, Tool: "dock " + version.Version, Created: now()}
	if digest := toImageScan(scan, "").Digest; strings.HasPrefix(digest, "sha256:") {
		img.Digest = digest
	}
	return SBOMExport{Name: name, Write: func(w io.Writer) error {
		defer func() { _ = f.Close() }()
		zr, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("read the stored SBOM: %w", err)
		}
		zw := gzip.NewWriter(w)
		return errors.Join(sbom.Convert(zw, io.LimitReader(zr, node.MaxSBOMBytes), format, img), zw.Close())
	}}, nil
}
