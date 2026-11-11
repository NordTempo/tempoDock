package core

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Alerts on new vulnerabilities (ADR 0074, phase 2). A live release
// whose image has critical vulnerabilities (or high, as the org chose)
// it didn't have gets one alert per service: warning, critical when CISA
// lists one as exploited. "Didn't have" is against the same image's
// previous match (the daily refresh found a new CVE) or, for an image new
// to the service, against the image of its release before; a service's
// first scanned image alerts nothing. No reminders: what stands is on
// the Images page. The alert resolves by itself once no vulnerability it
// names is in the live image any more (a deploy replaced it) or all are
// ignored.
//
// Everything here runs after a match, on the scanner's goroutine, or
// after an ignore or the setting changed: never on a deploy's path.

const vulnAlertKey = "vulns:"

// vulnCheck says what a live release's vulnerabilities are new against.
type vulnCheck int

const (
	// vulnRecheck: nothing is new; drop what no longer applies.
	vulnRecheck vulnCheck = iota
	// vulnRematched: the image was matched again; fresh are new.
	vulnRematched
	// vulnNewImage: the image is new to the release's service; new
	// against the service's previous scanned image.
	vulnNewImage
)

// findingKey names a finding across matches and images: its ID and
// package.
type findingKey struct{ id, pkg string }

// vulnLevel is the least severity that alerts for setting, 0 for off.
func vulnLevel(setting string) node.Severity {
	switch apitypes.VulnAlerts(setting) {
	case apitypes.VulnAlertsOff:
		return 0
	case apitypes.VulnAlertsHigh:
		return node.SeverityHigh
	}
	return node.SeverityCritical
}

// vulnAlerts brings the vulnerability alerts of the scan's live releases
// up to date (only the service's, when only is set). fresh are the
// findings a rematch found that the previous match didn't.
func (c *Core) vulnAlerts(ctx context.Context, scan store.ImageScan, check vulnCheck, fresh map[findingKey]bool, only int64) {
	ctx = context.WithoutCancel(ctx)
	uses, err := c.store.ListImageScanLiveUses(ctx, &scan.ID)
	if err != nil {
		c.log.Warn("vulnerability alerts: list releases", "scan", scan.ID, "err", err)
		return
	}
	settings := map[int64]node.Severity{}
	ignores := map[int64]ignoreSet{}
	var found []store.ListScanFindingsAtLeastRow
	loaded := false
	for _, u := range uses {
		if only != 0 && u.ServiceID != only {
			continue
		}
		level, ok := settings[u.OrgID]
		if !ok {
			s, err := c.store.GetOrgVulnAlerts(ctx, u.OrgID)
			if err != nil {
				continue
			}
			level = vulnLevel(s)
			settings[u.OrgID] = level
		}
		key := vulnAlertKey + alertServiceKey(u.ServiceID, "", "", "")
		if level == 0 {
			c.resolveAlert(ctx, key, time.Now())
			continue
		}
		if !loaded {
			// Critical and high once, for every org's setting.
			found, err = c.store.ListScanFindingsAtLeast(ctx, store.ListScanFindingsAtLeastParams{ScanIds: []int64{scan.ID}, MinSeverity: int64(node.SeverityHigh)})
			if err != nil {
				c.log.Warn("vulnerability alerts: read findings", "scan", scan.ID, "err", err)
				return
			}
			loaded = true
		}
		ig, ok := ignores[u.OrgID]
		if !ok {
			if ig, err = c.ignoresOf(ctx, u.OrgID); err != nil {
				continue
			}
			ignores[u.OrgID] = ig
		}
		at := vulnUse{u.ProjectID, u.ServiceID}
		var current []store.ListScanFindingsAtLeastRow
		for _, f := range found {
			if f.Severity >= int64(level) && !ig.hides(f.VulnID, f.Package, at) {
				current = append(current, f)
			}
		}
		var isNew func(store.ListScanFindingsAtLeastRow) bool
		switch check {
		case vulnRematched:
			isNew = func(f store.ListScanFindingsAtLeastRow) bool { return fresh[findingKey{f.VulnID, f.Package}] }
		case vulnNewImage:
			before, err := c.previousFindings(ctx, u.ServiceID, u.Number)
			if err != nil {
				continue
			}
			if before != nil {
				isNew = func(f store.ListScanFindingsAtLeastRow) bool { return !before[findingKey{f.VulnID, f.Package}] }
			}
		}
		c.raiseVulnAlert(ctx, key, scan, u, current, isNew)
	}
}

// previousFindings are the critical and high findings of the service's
// scanned image before release number, nil when it had none scanned
// (its first: nothing is new).
func (c *Core) previousFindings(ctx context.Context, serviceID, number int64) (map[findingKey]bool, error) {
	prev, err := c.store.PreviousServiceScan(ctx, store.PreviousServiceScanParams{ServiceID: serviceID, Number: number})
	if store.IsNotFound(err) || err == nil && prev == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListScanFindingsAtLeast(ctx, store.ListScanFindingsAtLeastParams{ScanIds: []int64{*prev}, MinSeverity: int64(node.SeverityHigh)})
	if err != nil {
		return nil, err
	}
	out := make(map[findingKey]bool, len(rows))
	for _, f := range rows {
		out[findingKey{f.VulnID, f.Package}] = true
	}
	return out, nil
}

// raiseVulnAlert opens, updates or resolves a service's vulnerability
// alert: what it already names and the live image still has, plus what
// isNew says is new (nil: nothing is).
func (c *Core) raiseVulnAlert(ctx context.Context, key string, scan store.ImageScan, u store.ListImageScanLiveUsesRow, current []store.ListScanFindingsAtLeastRow, isNew func(store.ListScanFindingsAtLeastRow) bool) {
	c.alerts.mu.Lock()
	open, isOpen := c.alerts.open[key]
	c.alerts.mu.Unlock()
	var named []string
	if isOpen {
		if t := toAlert(open).Target; t != nil {
			named = t.Vulnerabilities
		}
	}
	var keep []store.ListScanFindingsAtLeastRow
	added := false
	seen := map[string]bool{}
	for _, f := range current {
		if seen[f.VulnID] {
			continue
		}
		switch {
		case slices.Contains(named, f.VulnID):
		case isNew != nil && isNew(f):
			added = true
		default:
			continue
		}
		seen[f.VulnID] = true
		keep = append(keep, f)
	}
	switch {
	case len(keep) == 0:
		c.resolveAlert(ctx, key, time.Now())
		return
	case !isOpen && !added:
		return
	}
	c.raiseAlert(ctx, vulnAlert(key, scan, u, keep), time.Now())
}

// vulnAlert is the alert for a service's new vulnerabilities, worst
// first: "New critical vulnerability in shop/production/web",
// "CVE-2026-31337 in openssl 3.3.1-r0, known exploited. Fixed by
// rebuilding."
func vulnAlert(key string, scan store.ImageScan, u store.ListImageScanLiveUsesRow, found []store.ListScanFindingsAtLeastRow) alertSignal {
	svc := serviceKey(u.Project, u.Environment, u.Service)
	slices.SortStableFunc(found, func(x, y store.ListScanFindingsAtLeastRow) int {
		if x.Severity != y.Severity {
			return int(y.Severity - x.Severity)
		}
		return int(y.Kev - x.Kev)
	})
	ids := make([]string, len(found))
	nodes := make([]node.Finding, len(found))
	kev, crit := 0, 0
	for i, f := range found {
		ids[i] = f.VulnID
		nodes[i] = node.Finding{ID: f.VulnID, Severity: node.Severity(f.Severity), Package: f.Package, Type: f.PkgType, FixedIn: f.FixedIn}
		if f.Kev != 0 {
			kev++
		}
		if f.Severity == int64(node.SeverityCritical) {
			crit++
		}
	}
	grade := "critical"
	switch {
	case crit == 0:
		grade = "high"
	case crit < len(found):
		grade = "critical and high"
	}
	title := "New " + grade + " vulnerability in " + svc
	var msg string
	if len(found) == 1 {
		msg = fmt.Sprintf("%s in %s %s", found[0].VulnID, found[0].Package, found[0].Version)
		if kev > 0 {
			msg += ", known exploited"
		}
	} else {
		title = fmt.Sprintf("%d new %s vulnerabilities in %s", len(found), grade, svc)
		names := make([]string, 0, 2)
		for _, f := range found[:min(2, len(found))] {
			names = append(names, fmt.Sprintf("%s (%s)", f.VulnID, f.Package))
		}
		msg = strings.Join(names, ", ")
		if rest := len(found) - len(names); rest > 0 {
			msg += " and " + strconv.Itoa(rest) + " more"
		}
		if kev > 0 {
			msg += fmt.Sprintf("; %d known exploited", kev)
		}
	}
	switch countFindings(nodes).worstFix {
	case apitypes.FixRebuild:
		msg += ". Fixed by rebuilding."
	case apitypes.FixUpgrade:
		msg += ". Fixed by upgrading the app's dependencies."
	default:
		msg += ". No fix is known yet."
	}
	severity := apitypes.SeverityWarning
	if kev > 0 {
		severity = apitypes.SeverityCritical
	}
	return alertSignal{
		Key: key, Kind: apitypes.AlertNewVulnerability, Severity: severity, Title: title, Message: msg,
		Command: "dock vulns " + svcFlags(u.Project, u.Environment, u.Service),
		Target:  &apitypes.AlertTarget{Image: scan.ID, Vulnerabilities: ids},
		Project: u.Project, Environment: u.Environment, Service: u.Service, projectID: u.ProjectID, serviceID: u.ServiceID,
	}
}

// recheckVulnAlerts brings the org's open vulnerability alerts in line
// with its ignores and setting, after either changed: one live release
// read per open alert.
func (c *Core) recheckVulnAlerts(ctx context.Context, org int64) {
	var services []int64
	c.alerts.mu.Lock()
	for _, a := range c.alerts.open {
		if a.Kind == string(apitypes.AlertNewVulnerability) && alertOrgID(a) == org && a.ServiceID != nil {
			services = append(services, *a.ServiceID)
		}
	}
	c.alerts.mu.Unlock()
	for _, id := range services {
		r, err := c.store.GetActiveRelease(ctx, id)
		if err != nil || r.ImageScanID == nil {
			if store.IsNotFound(err) {
				c.resolveAlert(ctx, vulnAlertKey+alertServiceKey(id, "", "", ""), time.Now())
			}
			continue
		}
		scan, err := c.store.GetImageScan(ctx, *r.ImageScanID)
		if err != nil {
			continue
		}
		c.vulnAlerts(ctx, scan, vulnRecheck, nil, id)
	}
}

// VulnAlertSetting is the org's vulnerability alert setting. Scope: read.
func (c *Core) VulnAlertSetting(ctx context.Context, a Actor) (apitypes.VulnAlertSettings, error) {
	if err := a.require(apitypes.ScopeRead, "reading alert settings"); err != nil {
		return apitypes.VulnAlertSettings{}, err
	}
	s, err := c.store.GetOrgVulnAlerts(ctx, a.OrgID)
	return apitypes.VulnAlertSettings{Alert: apitypes.VulnAlerts(s)}, err
}

// SetVulnAlerts sets which new vulnerabilities in live images alert:
// critical, high (and critical) or off. Open alerts follow at once.
// Admins, like the channels.
func (c *Core) SetVulnAlerts(ctx context.Context, a Actor, req apitypes.VulnAlertSettings) (apitypes.VulnAlertSettings, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "changing alert settings"); err != nil {
		return apitypes.VulnAlertSettings{}, err
	}
	switch req.Alert {
	case apitypes.VulnAlertsCritical, apitypes.VulnAlertsHigh, apitypes.VulnAlertsOff:
	default:
		return apitypes.VulnAlertSettings{}, invalid("alert on critical, high or off, not %q", req.Alert)
	}
	if err := c.store.SetOrgVulnAlerts(ctx, store.SetOrgVulnAlertsParams{VulnAlerts: string(req.Alert), ID: a.OrgID}); err != nil {
		return apitypes.VulnAlertSettings{}, err
	}
	c.audit(ctx, a, "alerts.vulns", nil, nil, "vulnerabilities", "vulnerability alerts: "+string(req.Alert))
	c.recheckVulnAlerts(ctx, a.OrgID)
	return req, nil
}
