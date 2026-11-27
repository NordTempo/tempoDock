package core

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Image vulnerability scanning (ADR 0074). The rule everything here
// serves: a scan never makes a build or deploy slower. So
//
//   - every build, deploy, wake and environment move counts itself busy
//     (scanBusy/scanDone) for as long as it runs: a lock and a counter,
//     and the running scan's cancel, which kills its job container on the
//     builder. That is all a deploy pays: it takes nothing the scanner
//     holds and waits for nothing;
//   - the scanner starts work only when nothing has been busy anywhere on
//     the platform for [scan] quiet_seconds, one job at a time, and
//     checks again before each step;
//   - a preempted scan goes back to the queue as it was, without counting
//     as an attempt.
//
// The worker is one goroutine that exists only while there is work, like
// the job scheduler: a deploy ending kicks it (its release may have gone
// live with an image never scanned), it waits for the quiet minute,
// attaches live releases to scans (one per image), runs what is queued
// and exits. With nothing queued an idle daemon has no goroutine and no
// timer for scanning.

// Backoff of a failed scan: 10 minutes doubling to a day, five attempts.
const (
	scanRetryFirst = 10 * time.Minute
	scanRetryMax   = 24 * time.Hour
	scanAttempts   = 5
)

// scanSyncBatch bounds the live releases one pass attaches to scans.
const scanSyncBatch = 200

// scanState is the scanner's state.
type scanState struct {
	mu sync.Mutex
	// busy counts the builds, deploys, wakes and moves in flight;
	// lastBusy is when the last one ended (or the daemon started).
	busy     int
	lastBusy time.Time
	// cancel ends the running scan step; nil when none runs.
	cancel context.CancelFunc
	// running: the worker goroutine exists. kick wakes it.
	running bool
	kick    chan struct{}
	// enabled mirrors platform_settings.scanning.
	enabled bool
	// db is each builder's vulnerability database build time, once known.
	db map[int64]time.Time
	// quiet overrides quietFor (Options.ScanQuiet).
	quiet time.Duration
	// refreshAt is when the next builder's database is due for its daily
	// refresh, or the tags for their daily check (tagCheckDue), whichever
	// is first, as the scanner's last pass saw them; zero for none known
	// (the start-up pass looks).
	refreshAt time.Time
	// imported is the sha256 of the database archive an operator imported
	// last, "" for none (ADR 0074 phase 3).
	imported string
	// tagsChecked is when the daily check of base images' tags last
	// finished (checkTags; platform_settings.tags_checked_at); zero for
	// never.
	tagsChecked time.Time
}

func newScanState() scanState {
	return scanState{kick: make(chan struct{}, 1), db: map[int64]time.Time{}}
}

// scanBusy marks a build, deploy, wake or move in flight until scanDone:
// a running scan step is cancelled (its job container killed) and none
// starts until the platform has been quiet again. It never blocks: one
// uncontended lock and a cancel (BenchmarkScanBusy).
func (c *Core) scanBusy() {
	s := &c.scans
	s.mu.Lock()
	s.busy++
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

// scanDone ends what scanBusy marked and wakes the scanner, which waits
// for the quiet period before it does anything.
func (c *Core) scanDone() {
	s := &c.scans
	s.mu.Lock()
	s.busy--
	s.lastBusy = time.Now()
	s.mu.Unlock()
	c.kickScans()
}

// kickScans wakes the scanner, starting it if it isn't running and
// scanning is on. A kick after Close does nothing.
func (c *Core) kickScans() {
	s := &c.scans
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.running:
		select {
		case s.kick <- struct{}{}:
		default:
		}
	case s.enabled && c.ctx != nil && c.ctx.Err() == nil:
		s.running = true
		c.wg.Add(1)
		c.goCaught("scanning images", func() { c.runScans() })
	}
}

// startScans loads the setting and starts the scanner once, at start-up:
// scans a stop cut short are queued again, and the first pass waits a
// quiet period like any other (recovery may still be deploying).
func (c *Core) startScans(ctx context.Context) {
	on, err := c.store.GetScanning(ctx)
	if store.IsNotFound(err) {
		on, err = 1, nil
	}
	if err != nil {
		c.log.Error("read the scanning setting", "err", err)
		return
	}
	if err := c.store.RequeueInterruptedScans(ctx); err != nil {
		c.log.Error("requeue scans", "err", err)
	}
	// SBOMs an interrupted scan was writing; the scan runs again.
	tmps, _ := filepath.Glob(filepath.Join(c.cfg.DataDir, "sboms", ".sbom-*"))
	for _, f := range tmps {
		_ = os.Remove(f)
	}
	imported, err := c.store.GetScanDatabaseImport(ctx)
	if err != nil && !store.IsNotFound(err) {
		c.log.Error("read the imported database", "err", err)
	}
	// The last finished daily tag check: a day after it the next is due,
	// also across restarts. A check a restart cut short never recorded
	// one, so it runs again.
	var tagsChecked time.Time
	if last, err := c.store.GetTagsChecked(ctx); err != nil && !store.IsNotFound(err) {
		c.log.Error("read the last tag check", "err", err)
	} else if last > 0 {
		tagsChecked = time.Unix(last, 0)
	}
	c.scans.mu.Lock()
	c.scans.enabled = on != 0
	c.scans.imported = imported
	c.scans.tagsChecked = tagsChecked
	c.scans.lastBusy = time.Now()
	c.scans.mu.Unlock()
	c.kickScans()
}

// quietFor is how long the platform must be quiet before a scan step.
func (c *Core) quietFor() time.Duration {
	if c.scans.quiet > 0 {
		return c.scans.quiet
	}
	if q := c.cfg.Scan.QuietSeconds; q > 0 {
		return time.Duration(q) * time.Second
	}
	return time.Minute
}

// scanWait says how long the scanner must still wait: 0 to go on now,
// -1 for as long as something is busy (until the next kick).
func (c *Core) scanWait() time.Duration {
	s := &c.scans
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy > 0 {
		return -1
	}
	if left := c.quietFor() - time.Since(s.lastBusy); left > 0 {
		return left
	}
	return 0
}

// scanStep returns a context for one scan step, registered so scanBusy
// cancels it, or ok false when something became busy meanwhile.
func (c *Core) scanStep() (ctx context.Context, end func(), ok bool) {
	s := &c.scans
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy > 0 || !s.enabled {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(c.ctx)
	s.cancel = cancel
	return ctx, func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}, true
}

// runScans is the scanner's goroutine: it waits for quiet, passes, and
// exits when nothing is left (stopScans).
func (c *Core) runScans() {
	defer c.wg.Done()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		wait := c.scanWait()
		if wait == 0 {
			// A panic in a pass ends that pass: the next comes in a minute.
			more, retry := false, now().Add(time.Minute)
			c.safely("a pass of the image scanner", func() { more, retry = c.scanPass() })
			if c.ctx.Err() != nil {
				return
			}
			if more {
				continue
			}
			if retry.IsZero() {
				if c.stopScans() {
					return
				}
				continue // a kick came in during the pass
			}
			wait = max(time.Until(retry), time.Second)
		}
		var wake <-chan time.Time
		if wait > 0 {
			timer.Reset(wait)
			wake = timer.C
		}
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-c.scans.kick:
			timer.Stop()
		case <-wake:
		}
	}
}

// stopScans marks the scanner stopped unless a kick came in since its
// pass (then the caller passes again). Kicks check under the same lock.
func (c *Core) stopScans() bool {
	s := &c.scans
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.kick:
		return false
	default:
		s.running = false
		return true
	}
}

// scanPass does one step: it attaches live releases to scans, then runs
// the next queued scan. more says to pass again (after checking quiet);
// retry, when set, is when a scan waiting for its next attempt is due.
func (c *Core) scanPass() (more bool, retry time.Time) {
	ctx, end, ok := c.scanStep()
	if !ok {
		return false, time.Time{} // busy again, or turned off: the next kick
	}
	defer end()
	if err := c.syncScans(ctx); err != nil {
		if ctx.Err() == nil {
			c.log.Error("attach releases to scans", "err", err)
		}
		return c.scanPreempted(ctx), time.Time{}
	}
	builders, prod, nonProd := c.scanBuilders(ctx)
	if len(builders) == 0 {
		return false, time.Time{} // a builder coming online kicks
	}
	if srv, ok := c.scanImportDue(builders); ok {
		c.importScanDatabase(ctx, srv)
		return true, time.Time{}
	}
	n := now()
	if srv, ok := c.scanRefreshDue(builders, n); ok {
		c.refreshScanDatabase(ctx, srv)
		return true, time.Time{}
	}
	if c.tagCheckDue(n) {
		c.checkTags(ctx, builders[0])
		return true, time.Time{}
	}
	row, err := c.store.NextImageScan(ctx, store.NextImageScanParams{Now: ptr(n.Unix()), ProductionBuilder: boolInt(prod), NonProductionBuilder: boolInt(nonProd)})
	if store.IsNotFound(err) {
		due, err := c.store.NextImageScanRetry(ctx, ptr(n.Unix()))
		if err != nil || due == 0 {
			return false, time.Time{}
		}
		return false, time.Unix(due, 0)
	}
	if err != nil {
		c.log.Error("next scan", "err", err)
		return false, time.Time{}
	}
	c.runScan(ctx, row, builders)
	return true, time.Time{}
}

// scanPreempted reports whether a step ended because something became
// busy (pass again once it is quiet) rather than shutting down.
func (c *Core) scanPreempted(ctx context.Context) bool {
	return ctx.Err() != nil && c.ctx.Err() == nil
}

// scanBuilders are the servers that may scan now (scanners), read once.
func (c *Core) scanBuilders(ctx context.Context) (out []store.Server, prod, nonProd bool) {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Error("list servers for scans", "err", err)
		return nil, false, false
	}
	return c.scanners(servers)
}

// scanners are the servers among servers that may scan now (scanning),
// and whether any of them may scan production and non-production images.
func (c *Core) scanners(servers []store.Server) (out []store.Server, prod, nonProd bool) {
	for s := range c.scanning(servers) {
		out = append(out, s)
		prod, nonProd = prod || s.ScansProduction, nonProd || s.ScansNonProduction
	}
	return out, prod, nonProd
}

// scanKinds says whether any server among servers may scan production
// and non-production images now, without collecting them.
func (c *Core) scanKinds(servers []store.Server) (prod, nonProd bool) {
	for s := range c.scanning(servers) {
		prod, nonProd = prod || s.ScansProduction, nonProd || s.ScansNonProduction
	}
	return prod, nonProd
}

// scanning yields the servers among servers that may scan now: online
// scanners (scanRoles) whose disk isn't past the prune threshold.
func (c *Core) scanning(servers []store.Server) iter.Seq[store.Server] {
	return func(yield func(store.Server) bool) {
		for s := range c.scanRoles(servers) {
			if c.connected(s.ID) == nil || c.diskFull(s.ID) {
				continue
			}
			if !yield(s) {
				return
			}
		}
	}
}

// scanRoles yields the builders among servers that scan some kind's
// images, online or not, each with the scans flags it acts on (scansKind):
// what an operator set apart from what it builds (ADR 0074 amendment
// 2026-11-27). With the registry, the control plane scans a kind only
// while no other server scans it, online or not: a scan can wait, and
// every scanner keeps a database of gigabytes. Without it images stay
// where they were built, so the control plane scans its own.
func (c *Core) scanRoles(servers []store.Server) iter.Seq[store.Server] {
	return func(yield func(store.Server) bool) {
		var remoteProd, remoteNonProd bool
		for _, s := range servers {
			if !s.Local && c.canBuild(s) && c.reg != nil {
				remoteProd = remoteProd || s.ScansProduction
				remoteNonProd = remoteNonProd || s.ScansNonProduction
			}
		}
		for _, s := range servers {
			if !c.canBuild(s) {
				continue
			}
			if s.Local {
				s.ScansProduction = s.ScansProduction && !remoteProd
				s.ScansNonProduction = s.ScansNonProduction && !remoteNonProd
			}
			if scansAny(s) && !yield(s) {
				return
			}
		}
	}
}

// scansKind reports whether an operator lets builder s scan images of
// environments of kind, apart from what it builds: a production image
// may carry what its build was given, so it goes only where that was
// chosen.
func scansKind(s store.Server, kind apitypes.EnvironmentKind) bool {
	if kind == apitypes.KindProduction {
		return s.ScansProduction
	}
	return s.ScansNonProduction
}

// scansAny reports whether s is a builder that scans some kind's images,
// and so keeps a vulnerability database.
func scansAny(s store.Server) bool {
	return s.Builder && (s.ScansProduction || s.ScansNonProduction)
}

// syncScans gives every live release without a scan its image's scan,
// adding the scan when the image is new. A CI image is pinned to its
// repo digest first, asked of the server that runs it (an inspect),
// after it went live and only while the platform is quiet.
func (c *Core) syncScans(ctx context.Context) error {
	rows, err := c.store.ListUnscannedReleases(ctx, scanSyncBatch)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		img, ok := c.scanImageOf(ctx, r)
		if !ok {
			continue
		}
		prod := apitypes.EnvironmentKind(r.EnvKind) == apitypes.KindProduction
		base, baseDigest := r.BaseImage, r.BaseDigest
		if base == "" {
			base, baseDigest = img.base, img.baseDigest
		}
		prio := int64(1)
		if prod {
			prio = 0
		}
		var attached store.ImageScan
		err := c.store.Tx(ctx, func(q *store.Queries) error {
			scan, err := q.GetImageScanByKey(ctx, img.key)
			switch {
			case store.IsNotFound(err):
				scan, err = q.AddImageScan(ctx, store.AddImageScanParams{
					ImageKey: img.key, PullRef: img.pull, LocalRef: img.local, BuiltOnServerID: img.builtOn,
					OrgID: r.OrgID, Production: boolInt(prod), Priority: prio, BaseImage: base, BaseDigest: baseDigest,
					CreatedAt: now().Unix(),
				})
				if err == nil && img.unsupported != "" {
					err = q.SetImageScanState(ctx, store.SetImageScanStateParams{State: string(apitypes.ScanUnsupported), Error: img.unsupported, ID: scan.ID})
				}
			case err == nil:
				err = q.RaiseImageScanPriority(ctx, store.RaiseImageScanPriorityParams{Production: boolInt(prod), Priority: prio, ID: scan.ID})
			}
			if err != nil {
				return err
			}
			attached = scan
			return q.SetReleaseImageScan(ctx, store.SetReleaseImageScanParams{ImageScanID: &scan.ID, ID: r.ID})
		})
		if err != nil {
			return err
		}
		// An image already scanned may bring the service new
		// vulnerabilities; then open Images pages and overviews learn of
		// the live image, its alerts settled.
		if attached.MatchedAt != nil {
			c.vulnAlerts(ctx, attached, vulnNewImage, nil, r.ServiceID)
		}
		c.publishScan(ctx, attached)
	}
	return nil
}

// scanImage is how a release's image is scanned: the key one scan is
// kept by, what a builder pulls, a local name tried first and the
// builder that built it. unsupported, when set, says why it can't be.
type scanImage struct {
	key, pull, local string
	builtOn          *int64
	unsupported      string
	// base and baseDigest are the tag an image deployed by tag follows
	// and the digest it was deployed at: it is its own base.
	base, baseDigest string
}

// scanImageOf works out a live release's scanImage; ok false means not
// now (its server is offline): the next pass tries again.
func (c *Core) scanImageOf(ctx context.Context, r store.ListUnscannedReleasesRow) (scanImage, bool) {
	switch {
	case r.ImageDigest != "" && c.reg != nil:
		// Built by Dock and in the registry.
		return scanImage{key: r.ImageDigest, pull: c.registryRef(r.ImageRef, r.ImageDigest), local: r.ImageRef, builtOn: r.BuiltOnServerID}, true
	case c.dockNamed(r.ImageRef) && r.ImageID != "":
		// Built by Dock without a registry: only on the server it was
		// built on, by its local ID.
		on := r.BuiltOnServerID
		if on == nil {
			on = &r.ServerID
		}
		return scanImage{key: "local:" + r.ImageID, local: r.ImageRef, builtOn: on}, true
	}
	pinned := r.PinnedRef
	if pinned == "" {
		if strings.Contains(r.ImageRef, "@sha256:") {
			pinned = r.ImageRef
		} else {
			rt := c.connected(r.ServerID)
			if rt == nil {
				return scanImage{}, false
			}
			d, err := rt.ImageDigest(ctx, r.ImageRef)
			if err != nil {
				if ctx.Err() == nil {
					c.log.Warn("read a live image's digest", "release", r.ID, "err", err)
				}
				return scanImage{}, false
			}
			pinned = d
		}
		if pinned != "" {
			if err := c.store.SetReleasePinnedRef(ctx, store.SetReleasePinnedRefParams{PinnedRef: pinned, ID: r.ID}); err != nil {
				return scanImage{}, false
			}
		}
	}
	if pinned == "" {
		return scanImage{key: "release:" + strconv.FormatInt(r.ID, 10),
			unsupported: "Dock can't tell which image this is: it has no registry digest. Deploy it by digest (--image name@sha256:...) to get it scanned"}, true
	}
	_, digest, _ := strings.Cut(pinned, "@")
	img := scanImage{key: digest, pull: pinned}
	if tag := imageTag(r.ImageRef); tag != "" {
		img.base, img.baseDigest = tag, digest
	}
	return img, true
}

// sbomPath is where an image's SBOM is kept: gzipped syft-json in the
// data dir, like build logs, and not in the control plane's backup (an
// SBOM can always be made again).
func (c *Core) sbomPath(scanID int64) string {
	return filepath.Join(c.cfg.DataDir, "sboms", strconv.FormatInt(scanID, 10)+".json.gz")
}

// scanLimits bound each scan job.
func (c *Core) scanLimits(timeout time.Duration) node.ScanLimits {
	mb := c.cfg.Scan.MemoryMB
	if mb <= 0 {
		mb = 1024
	}
	return node.ScanLimits{MemoryBytes: int64(mb) << 20, Timeout: timeout}
}

func (c *Core) scanImages() node.ScanImages {
	return node.ScanImages{Syft: c.cfg.Scan.SyftImage, Grype: c.cfg.Scan.GrypeImage, Tool: c.cfg.Backup.ResticImage}
}

// runScan runs one queued scan on a builder: its database first (once),
// the SBOM unless it has one, then the match, each step killed the
// moment something becomes busy.
func (c *Core) runScan(ctx context.Context, row store.ImageScan, builders []store.Server) {
	srv, ok := c.scanServer(row, builders)
	if !ok {
		// Only the server that has it can scan it, and it is offline or
		// can't take production images: wait for it like a failure.
		c.scanFailed(ctx, row, errors.New("the server with this image can't scan it now"))
		return
	}
	rt := c.connected(srv.ID)
	if rt == nil {
		return
	}
	c.setScanState(ctx, row, apitypes.ScanScanning, "")
	err := c.scanOn(ctx, row, srv, rt)
	switch {
	case err == nil:
		return
	case c.scanPreempted(ctx):
		// Something became busy: back in the queue as it was.
		c.setScanState(context.WithoutCancel(ctx), row, apitypes.ScanQueued, row.Error)
	case c.ctx.Err() != nil:
		// Shutting down: the next start queues it again.
	default:
		c.scanFailed(ctx, row, c.nodeErr(srv.ID, err))
	}
}

// scanServer picks the builder for a scan among scanBuilders': the one
// that built the image when it can take it, else any that may (production
// images only on builders that scan production). An image without a
// pull reference can only be scanned where it was built.
func (c *Core) scanServer(row store.ImageScan, builders []store.Server) (store.Server, bool) {
	kind := apitypes.KindNonProduction
	if row.Production != 0 {
		kind = apitypes.KindProduction
	}
	may := func(s store.Server) bool { return scansKind(s, kind) }
	for _, s := range builders {
		if row.BuiltOnServerID != nil && s.ID == *row.BuiltOnServerID && may(s) {
			return s, true
		}
	}
	if row.PullRef == "" {
		return store.Server{}, false
	}
	for _, s := range builders {
		if may(s) {
			return s, true
		}
	}
	return store.Server{}, false
}

// scanOn makes the image's SBOM on srv (unless it has one) and matches
// it there.
func (c *Core) scanOn(ctx context.Context, row store.ImageScan, srv store.Server, rt node.Runtime) error {
	labels := node.Labels{ControlPlane: c.install}
	if err := c.ensureScanDatabase(ctx, srv, rt, labels); err != nil {
		return err
	}
	path := c.sbomPath(row.ID)
	if _, err := os.Stat(path); err != nil {
		if err := c.makeSBOM(ctx, row, srv, rt, labels, path); err != nil {
			return err
		}
	}
	return c.matchSBOM(ctx, row, rt, labels, path)
}

// ensureScanDatabase makes sure srv has a vulnerability database,
// downloading one the first time (Grype's, about 200 MB, 3.4 GB on disk).
func (c *Core) ensureScanDatabase(ctx context.Context, srv store.Server, rt node.Runtime, labels node.Labels) error {
	c.scans.mu.Lock()
	built := c.scans.db[srv.ID]
	c.scans.mu.Unlock()
	if !built.IsZero() {
		return nil
	}
	db, err := rt.ScanDatabase(ctx, node.ScanDatabaseRequest{Images: c.scanImages(), Labels: labels})
	if err == nil && db.Built.IsZero() && c.cfg.Scan.Offline {
		err = errors.New("this server has no vulnerability database, and [scan] offline keeps it from downloading one: import one with `dock scanning import FILE`")
	}
	if err == nil && db.Built.IsZero() {
		db, err = rt.ScanDatabase(ctx, node.ScanDatabaseRequest{Images: c.scanImages(), Update: true, Labels: labels})
		if err == nil {
			c.setScanChecked(srv.ID)
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			c.setServerScanError(srv.ID, nil, err.Error())
		}
		return fmt.Errorf("vulnerability database: %w", err)
	}
	c.scans.mu.Lock()
	c.scans.db[srv.ID] = db.Built
	c.scans.mu.Unlock()
	c.setServerScanError(srv.ID, ptr(db.Built.Unix()), "")
	return nil
}

// scanRefreshEvery is how often a builder asks for a newer vulnerability
// database (Grype's is published daily).
const scanRefreshEvery = 24 * time.Hour

// scanRefreshDue picks a builder whose database is due for a refresh:
// it has one, and hasn't asked for a newer one for a day. It notes when
// the next one is due, for kickScanRefresh.
func (c *Core) scanRefreshDue(builders []store.Server, at time.Time) (store.Server, bool) {
	if c.cfg.Scan.Offline {
		return store.Server{}, false // databases only come by import
	}
	var next time.Time
	for _, s := range builders {
		if s.ScanDbBuiltAt == nil {
			continue // none yet: its first scan downloads one
		}
		due := at
		if s.ScanDbCheckedAt != nil {
			due = time.Unix(*s.ScanDbCheckedAt, 0).Add(scanRefreshEvery)
		}
		if !due.After(at) {
			return s, true
		}
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	c.scans.mu.Lock()
	c.scans.refreshAt = next
	c.scans.mu.Unlock()
	return store.Server{}, false
}

// refreshScanDatabase has a builder get a newer vulnerability database
// (a download of about 200 MB when there is one) and queues every live
// image matched against an older one to be matched again from its
// stored SBOM: no image is pulled or unpacked again. Like any step, a
// build or deploy kills it, and the next quiet minute tries again; a
// failure waits for the next day.
func (c *Core) refreshScanDatabase(ctx context.Context, srv store.Server) {
	rt := c.connected(srv.ID)
	if rt == nil {
		return
	}
	db, err := rt.ScanDatabase(ctx, node.ScanDatabaseRequest{Images: c.scanImages(), Update: true, Labels: node.Labels{ControlPlane: c.install}})
	if c.scanPreempted(ctx) || c.ctx.Err() != nil {
		return
	}
	c.setScanChecked(srv.ID)
	if err != nil {
		c.log.Warn("refresh the vulnerability database", "server", srv.Name, "err", err)
		c.setServerScanError(srv.ID, srv.ScanDbBuiltAt, "refreshing the vulnerability database: "+err.Error())
		return
	}
	c.scans.mu.Lock()
	c.scans.db[srv.ID] = db.Built
	c.scans.mu.Unlock()
	c.setServerScanError(srv.ID, ptr(db.Built.Unix()), "")
	n, err := c.store.RequeueStaleLiveScans(context.WithoutCancel(ctx), ptr(db.Built.Unix()))
	if err != nil {
		c.log.Error("queue images to match again", "err", err)
		return
	}
	c.log.Info("vulnerability database refreshed", "server", srv.Name, "built", db.Built, "rematch", n)
}

// setScanChecked records that a builder just asked for a newer database.
func (c *Core) setScanChecked(serverID int64) {
	if err := c.store.SetServerScanChecked(context.WithoutCancel(c.ctx), store.SetServerScanCheckedParams{ScanDbCheckedAt: ptr(now().Unix()), ID: serverID}); err != nil {
		c.log.Warn("record the scan database check", "server", serverID, "err", err)
	}
}

// kickScanRefresh starts the scanner when a builder's database is due
// for its daily refresh, or base images' tags for their daily check, as
// its last pass noted. The hourly retention pass calls it, so neither
// needs a timer of its own, and it costs the pass no query.
func (c *Core) kickScanRefresh() {
	c.scans.mu.Lock()
	due := c.scans.enabled && !c.scans.refreshAt.IsZero() && !now().Before(c.scans.refreshAt)
	c.scans.mu.Unlock()
	if due {
		c.kickScans()
	}
}

// setServerScanError records a builder's database and last scan error.
func (c *Core) setServerScanError(serverID int64, built *int64, msg string) {
	if err := c.store.SetServerScanDatabase(context.WithoutCancel(c.ctx), store.SetServerScanDatabaseParams{ScanDbBuiltAt: built, ScanError: msg, ID: serverID}); err != nil {
		c.log.Warn("record the scan database", "server", serverID, "err", err)
	}
}

// makeSBOM has srv make the image's SBOM and keeps it gzipped at path,
// written to a temporary file first so path is always whole.
func (c *Core) makeSBOM(ctx context.Context, row store.ImageScan, srv store.Server, rt node.Runtime, labels node.Labels, path string) error {
	req := node.SBOMRequest{Images: c.scanImages(), Pull: row.PullRef, Limits: c.scanLimits(10 * time.Minute), Labels: labels}
	if row.BuiltOnServerID != nil && *row.BuiltOnServerID == srv.ID {
		req.Image = row.LocalRef
	}
	if row.PullRef != "" {
		if repo, ok := c.inRegistry(row.PullRef); ok {
			req.PullOptions = node.PullOptions{Token: c.reg.Token("server "+srv.Name, repo, false)}
			release, err := c.useRegistry(ctx)
			if err != nil {
				return err
			}
			defer release()
		} else {
			login, _, err := c.registryLogin(ctx, row.OrgID, row.PullRef)
			if err != nil {
				return err
			}
			req.PullOptions = login
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sbom-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	zw := gzip.NewWriter(tmp)
	res, err := rt.SBOM(ctx, req, zw)
	err = errors.Join(err, zw.Close(), tmp.Close())
	if err != nil {
		return err
	}
	if fi, err := os.Stat(tmp.Name()); err == nil && fi.Size() > maxStoredSBOM {
		return fmt.Errorf("its SBOM is %d MB gzipped, over the %d MB Dock keeps", fi.Size()>>20, maxStoredSBOM>>20)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return c.store.SetImageScanSBOM(context.WithoutCancel(ctx), store.SetImageScanSBOMParams{
		Distro: res.Distro, Packages: int64(res.Packages), SbomBytes: res.Bytes, SbomAt: ptr(now().Unix()), ServerID: &srv.ID, ID: row.ID,
	})
}

// maxStoredSBOM caps a gzipped SBOM on the control plane's disk.
const maxStoredSBOM = 8 << 20

// matchSBOM matches the stored SBOM on rt and replaces the image's
// findings with what it found.
func (c *Core) matchSBOM(ctx context.Context, row store.ImageScan, rt node.Runtime, labels node.Labels, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read the stored SBOM: %w", err)
	}
	var found []node.Finding
	res, err := rt.Match(ctx, node.MatchRequest{Images: c.scanImages(), Limits: c.scanLimits(5 * time.Minute), Labels: labels},
		io.LimitReader(zr, node.MaxSBOMBytes), func(f node.Finding) error {
			found = append(found, f)
			return nil
		})
	if errors.Is(err, node.ErrNoScanDatabase) {
		// The database went away (the volume was removed): get it again.
		c.scans.mu.Lock()
		clear(c.scans.db)
		c.scans.mu.Unlock()
	}
	if err != nil {
		return err
	}
	fresh, err := c.writeFindings(context.WithoutCancel(ctx), row.ID, found, res)
	if err != nil {
		return err
	}
	if scan, err := c.store.GetImageScan(ctx, row.ID); err == nil {
		check := vulnRematched
		if row.MatchedAt == nil {
			check = vulnNewImage // its first match: new to every service running it
		}
		// Alerts first, so whoever the scan event wakes sees them settled.
		c.vulnAlerts(ctx, scan, check, fresh, 0)
		c.publishScan(ctx, scan)
	}
	return nil
}

// writeFindings replaces an image's findings in one transaction, keeping
// when each was first seen, and records its counts. fresh are the
// findings the image's previous match didn't have, for alerts.
func (c *Core) writeFindings(ctx context.Context, scanID int64, found []node.Finding, res node.MatchResult) (fresh map[findingKey]bool, err error) {
	t := now().Unix()
	counts := countFindings(found)
	var built *int64
	if !res.DatabaseBuilt.IsZero() {
		built = ptr(res.DatabaseBuilt.Unix())
	}
	fresh = map[findingKey]bool{}
	return fresh, c.store.Tx(ctx, func(q *store.Queries) error {
		clear(fresh) // a retried transaction starts over
		seen, err := q.ListImageFindingsSeen(ctx, scanID)
		if err != nil {
			return err
		}
		first := make(map[[3]string]int64, len(seen))
		for _, s := range seen {
			first[[3]string{s.VulnID, s.Package, s.Version}] = s.FirstSeenAt
		}
		if err := q.DeleteImageFindings(ctx, scanID); err != nil {
			return err
		}
		for _, f := range found {
			at, ok := first[[3]string{f.ID, f.Package, f.Version}]
			if !ok {
				at = t
				fresh[findingKey{f.ID, f.Package}] = true
			}
			if err := q.AddImageFinding(ctx, store.AddImageFindingParams{
				ScanID: scanID, Severity: int64(f.Severity), VulnID: f.ID, Package: f.Package, Version: f.Version,
				PkgType: f.Type, FixedIn: f.FixedIn, Kev: boolInt(f.KEV), Epss: f.EPSS, FirstSeenAt: at,
			}); err != nil {
				return err
			}
		}
		return q.SetImageScanResult(ctx, store.SetImageScanResultParams{
			MatchedAt: &t, DbBuiltAt: built, Critical: counts.critical, High: counts.high, Medium: counts.medium,
			Low: counts.low, Other: counts.other, Fixable: counts.fixable, Kev: counts.kev, Truncated: int64(res.Truncated),
			WorstFix: string(counts.worstFix), ID: scanID,
		})
	})
}

// findingCounts are an image's findings counted, and what fixes the worst.
type findingCounts struct {
	critical, high, medium, low, other, fixable, kev int64
	worstFix                                         apitypes.FixKind
}

// countFindings counts findings by severity and works out what fixes the
// worst of them (critical, else high): rebuilding when system packages
// among them have fixes, else upgrading the app's, else none known.
func countFindings(found []node.Finding) findingCounts {
	var n findingCounts
	worst := node.SeverityHigh
	for _, f := range found {
		switch f.Severity {
		case node.SeverityCritical:
			n.critical++
		case node.SeverityHigh:
			n.high++
		case node.SeverityMedium:
			n.medium++
		case node.SeverityLow:
			n.low++
		default:
			n.other++
		}
		if f.FixedIn != "" {
			n.fixable++
		}
		if f.KEV {
			n.kev++
		}
	}
	if n.critical > 0 {
		worst = node.SeverityCritical
	} else if n.high == 0 {
		return n
	}
	n.worstFix = apitypes.FixNone
	for _, f := range found {
		if f.Severity != worst || f.FixedIn == "" {
			continue
		}
		if systemPackage(f.Type) {
			return n.with(apitypes.FixRebuild)
		}
		n.worstFix = apitypes.FixUpgrade
	}
	return n
}

func (n findingCounts) with(k apitypes.FixKind) findingCounts { n.worstFix = k; return n }

// systemPackage reports whether a package type is the operating
// system's, which comes with the base image or the Dockerfile's
// installs.
func systemPackage(t string) bool {
	switch t {
	case "apk", "deb", "rpm", "alpm", "portage":
		return true
	}
	return false
}

// scanFailed counts a failed attempt: queued again with backoff, failed
// for good after scanAttempts.
func (c *Core) scanFailed(ctx context.Context, row store.ImageScan, err error) {
	ctx = context.WithoutCancel(ctx)
	attempts := row.Attempts + 1
	msg := err.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	p := store.SetImageScanStateParams{State: string(apitypes.ScanFailed), Error: msg, Attempts: attempts, ID: row.ID}
	if attempts < scanAttempts {
		wait := min(scanRetryFirst<<(attempts-1), scanRetryMax)
		p.State, p.NextAttemptAt = string(apitypes.ScanQueued), ptr(now().Add(wait).Unix())
	}
	c.log.Warn("image scan failed", "scan", row.ID, "image", row.ImageKey, "attempt", attempts, "err", err)
	if err := c.store.SetImageScanState(ctx, p); err != nil {
		c.log.Error("record a failed scan", "scan", row.ID, "err", err)
	}
	if scan, err := c.store.GetImageScan(ctx, row.ID); err == nil {
		c.publishScan(ctx, scan)
	}
}

// setScanState moves a scan to state, keeping its attempts, and tells
// whoever watches.
func (c *Core) setScanState(ctx context.Context, row store.ImageScan, state apitypes.ScanState, msg string) {
	err := c.store.SetImageScanState(ctx, store.SetImageScanStateParams{
		State: string(state), Error: msg, Attempts: row.Attempts, NextAttemptAt: row.NextAttemptAt, ID: row.ID,
	})
	if err != nil {
		c.log.Error("record a scan's state", "scan", row.ID, "err", err)
		return
	}
	row.State, row.Error = string(state), msg
	c.publishScan(ctx, row)
}

// publishScan sends a scan event for every live release of the image,
// each in its own org and project, the image named as that release
// knows it.
func (c *Core) publishScan(ctx context.Context, row store.ImageScan) {
	uses, err := c.store.ListImageScanLiveUses(context.WithoutCancel(ctx), &row.ID)
	if err != nil {
		return
	}
	for _, u := range uses {
		scan := toImageScan(row, c.imageName(u.ImageRef, apitypes.ImageUse{Project: u.Project, Service: u.Service, Release: u.Number}))
		c.bus.publish(busEvent{orgID: u.OrgID, projectID: u.ProjectID, envID: u.EnvironmentID, serviceID: u.ServiceID, ev: apitypes.Event{
			Type: apitypes.EventScan, Time: now(), Project: u.Project, Environment: u.Environment, Service: u.Service, Scan: &scan,
		}})
	}
}

// publishScans is publishScan for several images, their live releases
// read at once: the scans one tag check updated.
func (c *Core) publishScans(ctx context.Context, rows []store.ImageScan) {
	if len(rows) == 0 {
		return
	}
	ids := make([]*int64, len(rows))
	byID := make(map[int64]store.ImageScan, len(rows))
	for i, row := range rows {
		ids[i] = &row.ID
		byID[row.ID] = row
	}
	uses, err := c.store.ListImageScansLiveUses(context.WithoutCancel(ctx), ids)
	if err != nil {
		return
	}
	for _, u := range uses {
		row, ok := byID[*u.ImageScanID]
		if !ok {
			continue
		}
		scan := toImageScan(row, c.imageName(u.ImageRef, apitypes.ImageUse{Project: u.Project, Service: u.Service, Release: u.Number}))
		c.bus.publish(busEvent{orgID: u.OrgID, projectID: u.ProjectID, envID: u.EnvironmentID, serviceID: u.ServiceID, ev: apitypes.Event{
			Type: apitypes.EventScan, Time: now(), Project: u.Project, Environment: u.Environment, Service: u.Service, Scan: &scan,
		}})
	}
}

// trimScans deletes scans no release refers to any more, with their
// findings and SBOMs, on the hourly retention pass: one indexed read
// when there are none.
func (c *Core) trimScans(ctx context.Context) (int, error) {
	ids, err := c.store.ListOrphanImageScans(ctx, retentionBatch)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := os.Remove(c.sbomPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		err := c.store.Tx(ctx, func(q *store.Queries) error {
			if err := q.DeleteImageFindings(ctx, id); err != nil {
				return err
			}
			return q.DeleteImageScan(ctx, id)
		})
		if err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
