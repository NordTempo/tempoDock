package cli

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Image vulnerability scanning (ADR 0074): `dock vulns`, `dock images`,
// `dock scan`, `dock sbom` and the operators' `dock scanning`.

// newVulnsCmd is `dock vulns [-r N]`: the vulnerabilities in a service's
// live image, or with --all across the org's live releases.
func newVulnsCmd(g *globals) *cobra.Command {
	var f targetFlags
	var release int64
	var all, production, fixable bool
	var minimum string
	cmd := &cobra.Command{
		Use:   "vulns",
		Short: "Vulnerabilities in a service's live image, or with --all in every live release",
		Long: `The vulnerabilities in the image the service's live release runs (or release
N's with -r), grouped by what fixes them: rebuilding on a newer base
image (the operating system's packages) or upgrading the app's
dependencies. Images are scanned on builders once no build or deploy has
run for a minute, and matched again as the vulnerability database
updates.

With --all, every finding at or above --min in the organization's live
releases, worst first, with where it runs and the version that fixes it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if all {
				s, err := g.connect()
				if err != nil {
					return err
				}
				q := "?min=" + minimum
				if production {
					q += "&production=true"
				}
				r, err := get[apitypes.OrgFindings](ctx, s, apiPrefix+"/org/vulnerabilities"+q)
				if err != nil {
					return err
				}
				if fixable {
					r.Findings = keepFixable(r.Findings)
				}
				return g.io.Print(r, func() error { return printOrgFindings(g, r) })
			}
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			path := t.servicePath() + "/vulnerabilities"
			if release > 0 {
				path += "?release=" + strconv.FormatInt(release, 10)
			}
			d, err := get[apitypes.ImageDetail](ctx, s, path)
			if err != nil {
				return err
			}
			if fixable {
				d.Findings = keepFixableFindings(d.Findings)
			}
			if lvl := severityRank(apitypes.VulnSeverity(minimum)); cmd.Flags().Changed("min") {
				var keep []apitypes.Finding
				for _, fd := range d.Findings {
					if severityRank(fd.Severity) >= lvl {
						keep = append(keep, fd)
					}
				}
				d.Findings = keep
			}
			return g.io.Print(d, func() error { return printImageDetail(g, d, t) })
		},
	}
	f.add(cmd, needService)
	cmd.Flags().Int64VarP(&release, "release", "r", 0, "release number (default: the live one)")
	cmd.Flags().BoolVar(&all, "all", false, "every live release in the organization")
	cmd.Flags().BoolVar(&production, "production", false, "with --all: only production environments")
	cmd.Flags().BoolVar(&fixable, "fixable", false, "only findings with a fixed version")
	cmd.Flags().StringVar(&minimum, "min", "high", "lowest severity to show: critical, high, medium, low (with --all, or to narrow a service's list)")
	cmd.AddCommand(newVulnIgnoreCmd(g), newVulnIgnoresCmd(g), newVulnUnignoreCmd(g))
	return cmd
}

// newVulnIgnoreCmd is `dock vulns ignore <id>`: leave a vulnerability
// out where it doesn't matter, with why.
func newVulnIgnoreCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req apitypes.IgnoreVulnRequest
	var in string
	cmd := &cobra.Command{
		Use:   "ignore <vulnerability> --reason <why>",
		Short: "Ignore a vulnerability in a service, a project or the organization",
		Long: `Leave a vulnerability out of counts, the service overview, the Images page
and alerts: in the service (default; deploy on its environment), with
--in project every service of the project (deploy on the project), or
with --in org the whole organization (admin). --package limits it to one
package. It holds --days (90 by default, at most 365); admins can make
it --always. Its findings stay listed, as ignored, and nothing is
scanned again. Ignoring the same one in the same place again replaces
the reason and expiry.`,
		Example: `  dock vulns ignore CVE-2026-22451 --package sharp --reason "we only resize images we generate"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Vulnerability, req.Scope = args[0], apitypes.IgnoreScope(in)
			var s *session
			var err error
			switch req.Scope {
			case apitypes.IgnoreOrg:
				s, err = g.connect()
			case apitypes.IgnoreProject:
				var t target
				s, t, err = g.open(f, needProject)
				req.Project = t.Project
			case apitypes.IgnoreService:
				var t target
				s, t, err = g.open(f, needService)
				req.Project, req.Environment, req.Service = t.Project, t.Environment, t.Service
			default:
				return usageError("--in is service, project or org", "Ignore it in the service (the default), with --in project, or with --in org.")
			}
			if err != nil {
				return err
			}
			ig, err := send[apitypes.VulnIgnore](cmd.Context(), s, http.MethodPost, apiPrefix+"/org/vulnerabilities/ignores", req)
			if err != nil {
				return err
			}
			return g.io.Print(ig, func() error {
				until := "until " + ignoreUntil(ig)
				if ig.Expires == nil {
					until = "for good"
				}
				g.io.Successf("Ignoring %s for %s %s", ig.Vulnerability, ignoreWhere(ig), until)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().StringVar(&in, "in", "service", "where: service, project or org")
	cmd.Flags().StringVar(&req.Package, "package", "", "only in this package")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "why it doesn't matter here (required)")
	cmd.Flags().IntVar(&req.Days, "days", 0, "how many days it holds, 1 to 365 (default 90)")
	cmd.Flags().BoolVar(&req.Always, "always", false, "never expire (admins)")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

// ignoreWhere says where an ignore applies.
func ignoreWhere(ig apitypes.VulnIgnore) string {
	switch ig.Scope {
	case apitypes.IgnoreService:
		return ig.Project + "/" + ig.Environment + "/" + ig.Service
	case apitypes.IgnoreProject:
		return "every service of " + ig.Project
	}
	return "the whole organization"
}

// ignoreUntil is "2027-02-08", "always" or "expired 2026-11-02".
func ignoreUntil(ig apitypes.VulnIgnore) string {
	switch {
	case ig.Expires == nil:
		return "always"
	case ig.Expired:
		return "expired " + ig.Expires.Format("2006-01-02")
	}
	return ig.Expires.Format("2006-01-02")
}

// newVulnIgnoresCmd is `dock vulns ignores`: every ignore of the org.
func newVulnIgnoresCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "ignores",
		Short: "Every vulnerability ignore in the organization",
		Long: `Every ignore of the organization, newest first: the vulnerability, the
package, where it applies, why, until when and who made it. Expired
ignores no longer apply and are listed for 30 days.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			l, err := get[apitypes.VulnIgnores](cmd.Context(), s, apiPrefix+"/org/vulnerabilities/ignores")
			if err != nil {
				return err
			}
			return g.io.Print(l, func() error {
				if len(l.Ignores) == 0 {
					g.io.Detail("No ignores.")
					return nil
				}
				tbl := g.io.Table("id", "vulnerability", "package", "where", "why", "until", "by")
				for _, ig := range l.Ignores {
					pkg := ig.Package
					if pkg == "" {
						pkg = "any"
					}
					tbl.Row(itoa(ig.ID), ig.Vulnerability, pkg, ignoreWhere(ig), ig.Reason, ignoreUntil(ig), ig.By)
				}
				return tbl.Flush()
			})
		},
	}
}

// newVulnUnignoreCmd is `dock vulns unignore <id>`.
func newVulnUnignoreCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "unignore <id>",
		Short: "Stop ignoring a vulnerability: its findings count again",
		Long:  "Remove an ignore by its ID (`dock vulns ignores` lists them). It takes what making it took.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id < 1 {
				return usageError("the ignore ID is a number", "`dock vulns ignores` lists them.")
			}
			s, err := g.connect()
			if err != nil {
				return err
			}
			if err := del(cmd.Context(), s, apiPrefix+"/org/vulnerabilities/ignores/"+args[0]); err != nil {
				return err
			}
			g.io.Successf("Removed ignore %d", id)
			return nil
		},
	}
}

// newVulnAlertsCmd is `dock alerts vulns [critical|high|off]`.
func newVulnAlertsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "vulns [critical|high|off]",
		Short: "Which new vulnerabilities in live images alert: critical (default), high, or off",
		Long: `An alert when a live release gets a vulnerability at least this bad that it
didn't have: one per service, critical when CISA lists it as exploited,
never reminded of. It resolves once the live image no longer has it or
it is ignored. A service's first scanned image alerts nothing. Without an
argument, shows the setting; changing it takes an organization admin.`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"critical", "high", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/alerts/vulnerabilities"
			var v apitypes.VulnAlertSettings
			if len(args) == 0 {
				v, err = get[apitypes.VulnAlertSettings](cmd.Context(), s, path)
			} else {
				v, err = send[apitypes.VulnAlertSettings](cmd.Context(), s, http.MethodPut, path, apitypes.VulnAlertSettings{Alert: apitypes.VulnAlerts(args[0])})
			}
			if err != nil {
				return err
			}
			return g.io.Print(v, func() error {
				words := map[apitypes.VulnAlerts]string{
					apitypes.VulnAlertsCritical: "new critical vulnerabilities in live images alert",
					apitypes.VulnAlertsHigh:     "new critical and high vulnerabilities in live images alert",
					apitypes.VulnAlertsOff:      "vulnerabilities don't alert",
				}
				g.io.Detail(strings.ToUpper(words[v.Alert][:1]) + words[v.Alert][1:] + ".")
				return nil
			})
		},
	}
}

// severityRank orders severities, critical highest.
func severityRank(s apitypes.VulnSeverity) int {
	switch s {
	case apitypes.VulnCritical:
		return 5
	case apitypes.VulnHigh:
		return 4
	case apitypes.VulnMedium:
		return 3
	case apitypes.VulnLow:
		return 2
	case apitypes.VulnNegligible:
		return 1
	}
	return 0
}

func keepFixable(in []apitypes.OrgFinding) []apitypes.OrgFinding {
	out := in[:0]
	for _, f := range in {
		if f.FixedIn != "" {
			out = append(out, f)
		}
	}
	return out
}

func keepFixableFindings(in []apitypes.Finding) []apitypes.Finding {
	out := in[:0]
	for _, f := range in {
		if f.FixedIn != "" {
			out = append(out, f)
		}
	}
	return out
}

// countsText is "2 critical, 5 high, 11 more (6 fixable)".
func countsText(n apitypes.VulnCounts) string {
	var parts []string
	if n.Critical > 0 {
		parts = append(parts, fmt.Sprintf("%d critical", n.Critical))
	}
	if n.High > 0 {
		parts = append(parts, fmt.Sprintf("%d high", n.High))
	}
	if rest := n.Medium + n.Low + n.Other; rest > 0 {
		word := "more"
		if len(parts) == 0 {
			word = "medium or low"
		}
		parts = append(parts, fmt.Sprintf("%d %s", rest, word))
	}
	if len(parts) == 0 {
		return "none"
	}
	s := strings.Join(parts, ", ")
	if n.Fixable > 0 {
		s += fmt.Sprintf(" (%d fixable)", n.Fixable)
	}
	if n.KEV > 0 {
		s += fmt.Sprintf(", %d known exploited", n.KEV)
	}
	return s
}

// scanText says where a scan is, for one that isn't scanned.
func scanText(s apitypes.ImageScan) string {
	switch s.State {
	case apitypes.ScanScanned:
		return countsText(s.VulnCounts)
	case apitypes.ScanScanning:
		return "scanning"
	}
	if s.Error != "" {
		return string(s.State) + ": " + s.Error
	}
	return string(s.State)
}

// fixTitle names a fix group.
func fixTitle(g apitypes.FixGroup) string {
	switch g.Kind {
	case apitypes.FixRebuild:
		return "Rebuild on a newer base image"
	case apitypes.FixUpgrade:
		return fmt.Sprintf("Upgrade %s", plural(len(g.Packages), "package"))
	}
	return "No fix yet"
}

func printImageDetail(g *globals, d apitypes.ImageDetail, t target) error {
	sc := d.Scan
	scanned := ""
	if sc.ScannedAt != nil {
		scanned = ago(*sc.ScannedAt)
		if sc.DatabaseBuilt != nil {
			scanned += ", database of " + sc.DatabaseBuilt.Format("2006-01-02")
		}
	}
	base := sc.Distro
	if sc.Packages > 0 {
		base = strings.TrimSpace(fmt.Sprintf("%s, %s", sc.Distro, plural(sc.Packages, "package")))
		base = strings.TrimPrefix(base, ", ")
	}
	if sc.Base != nil {
		base = strings.TrimPrefix(base+", built on "+sc.Base.Image, ", ")
		if sc.Base.Moved {
			base += " (the tag has a newer image: a rebuild picks it up)"
		}
	}
	if err := g.io.Fields("image", sc.Image, "digest", sc.Digest, "base", base, "scanned", scanned, "vulnerabilities", scanText(sc)); err != nil {
		return err
	}
	if sc.State != apitypes.ScanScanned || len(d.Findings) == 0 {
		return nil
	}
	ignored := 0
	for _, fd := range d.Findings {
		if fd.Ignored {
			ignored++
		}
	}
	out := g.io.Out
	for _, grp := range d.Fixes {
		_, _ = fmt.Fprintf(out, "\n%s: %s\n", fixTitle(grp), countsText(grp.VulnCounts))
		for i, p := range grp.Packages {
			if i == 10 {
				_, _ = fmt.Fprintf(out, "  and %d more packages\n", len(grp.Packages)-10)
				break
			}
			fix := ""
			if p.FixedIn != "" {
				fix = " -> " + p.FixedIn
			}
			ids := strings.Join(p.Findings[:min(3, len(p.Findings))], ", ")
			if len(p.Findings) > 3 {
				ids += fmt.Sprintf(" +%d", len(p.Findings)-3)
			}
			kev := ""
			if p.KEV {
				kev = " (known exploited)"
			}
			_, _ = fmt.Fprintf(out, "  %s %s%s  %s %s%s\n", p.Package, p.Version, fix, p.Worst, ids, kev)
		}
	}
	if ignored > 0 {
		_, _ = fmt.Fprintf(out, "\n%s not counted: `dock vulns ignores`\n", plural(ignored, "ignored finding"))
	}
	_, _ = fmt.Fprintf(out, "\nEvery finding: `dock vulns %s --json`; scan again: `dock scan %s`\n", t.flags(), t.flags())
	return nil
}

func printOrgFindings(g *globals, r apitypes.OrgFindings) error {
	if len(r.Findings) == 0 {
		g.io.Detail("No findings at that severity in live releases.")
	} else {
		tbl := g.io.Table("severity", "vulnerability", "package", "fixed in", "service")
		for _, f := range r.Findings {
			id := f.ID
			if f.KEV {
				id += " (KEV)"
			}
			tbl.Row(string(f.Severity), id, f.Package+" "+f.Version, f.FixedIn, fmt.Sprintf("%s/%s/%s r%d", f.Project, f.Environment, f.Service, f.Release))
		}
		if err := tbl.Flush(); err != nil {
			return err
		}
	}
	for _, u := range r.Unscanned {
		g.io.Detail("Not scanned: " + u)
	}
	return nil
}

// newImagesCmd is `dock images`: every image the org's live releases run.
func newImagesCmd(g *globals) *cobra.Command {
	var production bool
	cmd := &cobra.Command{
		Use:   "images",
		Short: "Every image the organization's live releases run, and their vulnerabilities",
		Long: `Every image the organization's live releases run, one row per image however
many services run it, worst first: its base, its vulnerabilities by
severity and when it was scanned. ` + "`dock vulns`" + ` shows one service's.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/org/images"
			if production {
				path += "?production=true"
			}
			l, err := get[apitypes.ImageList](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			return g.io.Print(l, func() error { return printImages(g, l) })
		},
	}
	cmd.Flags().BoolVar(&production, "production", false, "only images in production environments")
	return cmd
}

func printImages(g *globals, l apitypes.ImageList) error {
	if len(l.Images) == 0 {
		g.io.Detail("No live releases.")
		return nil
	}
	tbl := g.io.Table("image", "used by", "base", "vulnerabilities", "update", "scanned")
	for _, img := range l.Images {
		var used []string
		for _, u := range img.UsedBy {
			used = append(used, u.Service+" · "+u.Environment)
		}
		base, vulns, update, scanned := "", "queued", "", ""
		if sc := img.Scan; sc != nil {
			base, vulns = sc.Distro, scanText(*sc)
			if sc.ScannedAt != nil {
				scanned = ago(*sc.ScannedAt)
			}
			if b := sc.Base; b != nil && b.Moved {
				update = "newer base"
				if b.Own {
					update = "newer image"
				}
			}
		}
		tbl.Row(img.Image, strings.Join(used, ", "), base, vulns, update, scanned)
	}
	if err := tbl.Flush(); err != nil {
		return err
	}
	if l.DatabaseBuilt != nil {
		g.io.Detail("Vulnerability database of " + l.DatabaseBuilt.Format("2006-01-02") + ".")
	}
	return nil
}

// newScanCmd is `dock scan [-r N]`: queue a service's image for a scan.
func newScanCmd(g *globals) *cobra.Command {
	var f targetFlags
	var release int64
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan a service's live image again",
		Long: `Queue the image of the service's live release (or release N) for a
vulnerability scan against the current database: from its SBOM when it
has one. Like every scan it waits until no build or deploy has run for a
minute, so it never slows one down.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			sc, err := send[apitypes.ImageScan](cmd.Context(), s, http.MethodPost, t.servicePath()+"/scan", apitypes.ScanRequest{Release: release})
			if err != nil {
				return err
			}
			return g.io.Print(sc, func() error {
				g.io.Detail(fmt.Sprintf("Queued %s for a scan; see it with `dock vulns %s`.", sc.Image, t.flags()))
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().Int64VarP(&release, "release", "r", 0, "release number (default: the live one)")
	return cmd
}

// newImageUpdatesCmd is `dock image-updates [auto|manual|check]`: a
// service's image updates (ADR 0074, amendment 2026-11-26).
func newImageUpdatesCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "image-updates [auto|manual|check]",
		Short: "Whether a service deployed by tag updates to the tag's newer image on its own",
		Long: `Without an argument, shows the tag the service's live image follows, whether
it points at a newer image and whether the service updates on its own.

  auto     after the daily check sees the tag point at a newer image, Dock
           snapshots the service's volumes and deploys that image by
           digest; a release that doesn't come up leaves the previous one
           serving
  manual   the default: the service offers Update, deployed by hand with
           ` + "`dock deploy --image TAG@DIGEST`" + `
  check    ask the registry now instead of waiting for the daily check`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"auto", "manual", "check"},
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			switch {
			case len(args) == 0:
			case args[0] == "check":
				if _, err := send[apitypes.ImageScan](ctx, s, http.MethodPost, t.servicePath()+"/image-check", nil); err != nil {
					return err
				}
			case args[0] == "auto" || args[0] == "manual":
				if _, err := send[apitypes.Service](ctx, s, http.MethodPut, t.servicePath()+"/auto-update", apitypes.AutoUpdateRequest{AutoUpdate: args[0] == "auto"}); err != nil {
					return err
				}
			default:
				return &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("unknown argument %q", args[0]), Hint: "Use `dock image-updates`, `auto`, `manual` or `check`."}
			}
			svc, err := get[apitypes.Service](ctx, s, t.servicePath())
			if err != nil {
				return err
			}
			// 404 until the live image is scanned: no tag known yet.
			detail, err := get[apitypes.ImageDetail](ctx, s, t.servicePath()+"/vulnerabilities")
			if e, ok := errors.AsType[*output.Error](err); err != nil && (!ok || e.Exit != output.ExitNotFound) {
				return err
			}
			st := imageUpdates{Service: svc.Name, AutoUpdate: svc.AutoUpdate, Base: detail.Scan.Base}
			return g.io.Print(st, func() error {
				printImageUpdates(g, st)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	return cmd
}

// imageUpdates is `dock image-updates --json`: whether the service
// updates on its own, and the tag its image follows (none before its
// first scan, or for an image deployed by digest).
type imageUpdates struct {
	Service    string              `json:"service"`
	AutoUpdate bool                `json:"auto_update"`
	Base       *apitypes.ImageBase `json:"base,omitempty"`
}

func printImageUpdates(g *globals, st imageUpdates) {
	mode := "manual"
	if st.AutoUpdate {
		mode = "automatic"
	}
	b := st.Base
	switch {
	case b == nil:
		g.io.Detail(fmt.Sprintf("Image updates: %s. Dock doesn't know which tag %s's image follows yet: it is known once the image is scanned, and only for images deployed by tag or built from a Dockerfile.", mode, st.Service))
		return
	case !b.Own:
		g.io.Detail(fmt.Sprintf("%s is built on %s.", st.Service, b.Image))
	default:
		g.io.Detail(fmt.Sprintf("%s follows %s. Image updates: %s.", st.Service, b.Image, mode))
	}
	checked := "not checked yet"
	if b.CheckedAt != nil {
		checked = "checked " + ago(*b.CheckedAt)
	}
	switch {
	case b.Moved && b.Own:
		g.io.Detail(fmt.Sprintf("The tag has a newer image (%s): `dock deploy -s %s --image %s@%s` updates it.", checked, st.Service, b.Image, b.Latest))
	case b.Moved:
		g.io.Detail(fmt.Sprintf("The tag has a newer image (%s): deploying again builds on it.", checked))
	default:
		g.io.Detail(fmt.Sprintf("Up to date (%s).", checked))
	}
}

// newSBOMCmd is `dock sbom [-r N]`: a service's image's SBOM to stdout.
func newSBOMCmd(g *globals) *cobra.Command {
	var f targetFlags
	var release int64
	var format string
	cmd := &cobra.Command{
		Use:   "sbom",
		Short: "Print the software bill of materials of a service's live image",
		Long: `Print the SBOM Dock made of the image the service's live release (or
release N) runs: every package Syft found, as Syft's JSON, or with
--format as CycloneDX 1.6 (cyclonedx-json) or SPDX 2.3 (spdx-json) JSON for
tools that take those. Redirect it to a file:
` + "`dock sbom --format cyclonedx-json > sbom.cdx.json`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			path := t.servicePath() + "/sbom?format=" + url.QueryEscape(format)
			if release > 0 {
				path += "&release=" + strconv.FormatInt(release, 10)
			}
			req, err := s.api.NewRequest(cmd.Context(), http.MethodGet, path, nil)
			if err != nil {
				return err
			}
			resp, err := s.api.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			zr, err := gzip.NewReader(resp.Body)
			if err != nil {
				return &output.Error{Exit: output.ExitError, Message: "the server sent an SBOM that isn't gzipped", Err: err}
			}
			_, err = io.Copy(g.io.Out, zr)
			return err
		},
	}
	f.add(cmd, needService)
	cmd.Flags().Int64VarP(&release, "release", "r", 0, "release number (default: the live one)")
	cmd.Flags().StringVar(&format, "format", "syft-json", "syft-json, cyclonedx-json or spdx-json")
	return cmd
}

// newScanningCmd is `dock scanning [on|off]`: image scanning on the
// platform (operators).
func newScanningCmd(g *globals) *cobra.Command {
	var removeDatabase bool
	show := func(cmd *cobra.Command, set *bool) error {
		s, err := g.connect()
		if err != nil {
			return err
		}
		var st apitypes.ScanningStatus
		if set == nil {
			st, err = get[apitypes.ScanningStatus](cmd.Context(), s, apiPrefix+"/scanning")
		} else {
			st, err = send[apitypes.ScanningStatus](cmd.Context(), s, http.MethodPut, apiPrefix+"/scanning",
				apitypes.ScanningSettings{Enabled: *set, RemoveDatabase: removeDatabase})
		}
		if err != nil {
			return err
		}
		return g.io.Print(st, func() error { return printScanning(g, st) })
	}
	cmd := &cobra.Command{
		Use:   "scanning",
		Short: "Image vulnerability scanning on the platform: on or off, each builder's database (operators)",
		Long: `Whether Dock scans live images for vulnerabilities, how many images are in
each state, and each builder's vulnerability database. Scans run only on
builders (production images only on builders that build production),
only once no build or deploy has run anywhere for a minute, and stop the
moment one starts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return show(cmd, nil) },
	}
	cmd.AddCommand(newScanningImportCmd(g))
	for _, on := range []bool{true, false} {
		word := map[bool]string{true: "on", false: "off"}[on]
		sub := &cobra.Command{
			Use: word, Args: cobra.NoArgs,
			Short: "Turn image scanning " + word,
			RunE:  func(cmd *cobra.Command, _ []string) error { return show(cmd, &on) },
		}
		if !on {
			sub.Long = `Stop scanning: a running scan stops at once and none starts; findings
already made stay. --remove-database also removes every builder's
vulnerability database (3.4 GB each), which ` + "`dock scanning on`" + ` downloads
again; with [scan] offline, import one again with ` + "`dock scanning import`" + ` (ADR 0101).`
			sub.Flags().BoolVar(&removeDatabase, "remove-database", false, "also remove every builder's vulnerability database (3.4 GB each)")
		}
		cmd.AddCommand(sub)
	}
	return cmd
}

// newScanningImportCmd is `dock scanning import FILE`: a database archive
// for builders that can't download one.
func newScanningImportCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "import FILE",
		Short: "Import a vulnerability database archive, for installs without internet access",
		Long: `Upload a Grype vulnerability database archive (download one where there
is internet: ` + "`grype db list`" + ` lists them, about 200 MB) for every builder to
import at the next quiet minute; live images are then matched against it.
Importing again replaces it. Set [scan] offline = true on the control plane
so builders never try to download one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return &output.Error{Exit: output.ExitUsage, Message: err.Error(), Hint: "Name a database archive file, like vulnerability-db_v6.tar.zst."}
			}
			defer func() { _ = f.Close() }()
			s, err := g.connect()
			if err != nil {
				return err
			}
			req, err := s.api.NewRequest(cmd.Context(), http.MethodPut, apiPrefix+"/scanning/database", f)
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/octet-stream")
			resp, err := s.api.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			var st apitypes.ScanningStatus
			if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
				return err
			}
			return g.io.Print(st, func() error {
				g.io.Successf("Imported; builders import it at the next quiet minute")
				return printScanning(g, st)
			})
		},
	}
}

func printScanning(g *globals, st apitypes.ScanningStatus) error {
	state := "off"
	if st.Enabled {
		state = "on"
	}
	var counts []string
	for _, s := range []apitypes.ScanState{apitypes.ScanScanned, apitypes.ScanQueued, apitypes.ScanScanning, apitypes.ScanFailed, apitypes.ScanUnsupported} {
		if n := st.Images[s]; n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, s))
		}
	}
	database := "downloaded by each builder, refreshed daily"
	if st.Offline {
		database = "offline: imported only (`dock scanning import FILE`)"
	}
	imported := ""
	if st.Imported != "" {
		imported = st.Imported[:min(len(st.Imported), 19)]
	}
	if err := g.io.Fields("scanning", state, "images", strings.Join(counts, ", "), "waiting", st.Waiting, "database", database, "imported", imported); err != nil {
		return err
	}
	if len(st.Builders) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(g.io.Out)
	tbl := g.io.Table("builder", "production", "database", "error")
	for _, b := range st.Builders {
		db := "none yet"
		if b.DatabaseBuilt != nil {
			db = b.DatabaseBuilt.Format("2006-01-02")
		}
		if b.Imported {
			db += " (imported)"
		}
		prod := "no"
		if b.Production {
			prod = "yes"
		}
		tbl.Row(b.Server, prod, db, b.Error)
	}
	return tbl.Flush()
}
