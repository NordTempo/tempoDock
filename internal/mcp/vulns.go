package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Image vulnerability scanning (ADR 0074), for agents: read only, with
// read permission. Ignoring a finding is for people and the CLI.

// addVulnTools registers list_vulnerabilities.
func (h *handler) addVulnTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "list_vulnerabilities", "List vulnerabilities in live images",
		"Vulnerabilities in live images and the version that fixes each: across the organization, or one service's image grouped by fix.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			EnvInput
			Service        string `json:"service,omitempty" doc:"Default: the whole organization."`
			ProductionOnly bool   `json:"production_only,omitempty"`
			MinSeverity    string `json:"min_severity,omitempty" doc:"critical, high (default), medium or low."`
			FixableOnly    bool   `json:"fixable_only,omitempty"`
		}) (mcpkit.Reply, error) {
			min := apitypes.VulnSeverity(in.MinSeverity)
			if min == "" {
				min = apitypes.VulnHigh
			}
			if in.Service != "" {
				ref, err := serviceOf(c, ServiceInput{EnvInput: in.EnvInput, Service: in.Service})
				if err != nil {
					return mcpkit.Reply{}, err
				}
				d, err := h.core.ServiceVulnerabilities(ctx, c.Actor, ref, 0)
				if err != nil {
					return mcpkit.Reply{}, err
				}
				return mcpkit.Reply{Summary: serviceVulnSummary(in.Service, d), Data: d}, nil
			}
			r, err := h.core.OrgFindings(ctx, c.Actor, in.ProductionOnly, min)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if in.FixableOnly {
				kept := r.Findings[:0]
				for _, f := range r.Findings {
					if f.FixedIn != "" {
						kept = append(kept, f)
					}
				}
				r.Findings = kept
			}
			return mcpkit.Reply{Summary: orgVulnSummary(r, in.ProductionOnly), Data: r}, nil
		})
}

// serviceVulnSummary answers for one service: the counts and what fixes
// the worst.
func serviceVulnSummary(service string, d apitypes.ImageDetail) string {
	sc := d.Scan
	if sc.State != apitypes.ScanScanned {
		return fmt.Sprintf("%s's live image (%s) is %s. %s", service, sc.Image, sc.State, sc.Error)
	}
	n := sc.VulnCounts
	sum := fmt.Sprintf("%s's live image (%s, %s) has %d critical, %d high, %d medium and %d low vulnerabilities; %d fixable.",
		service, sc.Image, sc.Distro, n.Critical, n.High, n.Medium, n.Low, n.Fixable)
	for _, g := range d.Fixes {
		if g.Kind == apitypes.FixNone || len(g.Packages) == 0 {
			continue
		}
		top := make([]string, 0, 3)
		for _, p := range g.Packages[:min(3, len(g.Packages))] {
			top = append(top, fmt.Sprintf("%s %s to %s (%s)", p.Package, p.Version, p.FixedIn, p.Worst))
		}
		how := "rebuild on a newer base image, or upgrade them in the Dockerfile"
		if g.Kind == apitypes.FixUpgrade {
			how = "upgrade the app's dependencies"
		}
		sum += fmt.Sprintf(" To fix %s, %s: %s.", fixCounts(g.VulnCounts), how, strings.Join(top, ", "))
	}
	return sum
}

// fixCounts is what a fix group fixes, worst first and only what it has:
// "2 critical and 13 high", "13 high", "22 medium or low".
func fixCounts(n apitypes.VulnCounts) string {
	var parts []string
	if n.Critical > 0 {
		parts = append(parts, fmt.Sprintf("%d critical", n.Critical))
	}
	if n.High > 0 {
		parts = append(parts, fmt.Sprintf("%d high", n.High))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d medium or low", n.Medium+n.Low+n.Other)
	}
	return strings.Join(parts, " and ")
}

// orgVulnSummary answers across the org: the worst few findings and where.
func orgVulnSummary(r apitypes.OrgFindings, production bool) string {
	where := "live releases"
	if production {
		where = "production"
	}
	if len(r.Findings) == 0 {
		sum := "No findings at that severity in " + where + "."
		if len(r.Unscanned) > 0 {
			sum += fmt.Sprintf(" %d services are not scanned yet.", len(r.Unscanned))
		}
		return sum
	}
	type key struct{ id, pkg string }
	seen := map[key][]string{}
	var order []key
	for _, f := range r.Findings {
		k := key{f.ID, f.Package}
		if _, ok := seen[k]; !ok {
			order = append(order, k)
		}
		seen[k] = append(seen[k], f.Project+"/"+f.Environment+"/"+f.Service)
	}
	top := make([]string, 0, 5)
	byKey := map[key]apitypes.OrgFinding{}
	for _, f := range r.Findings {
		if _, ok := byKey[key{f.ID, f.Package}]; !ok {
			byKey[key{f.ID, f.Package}] = f
		}
	}
	for _, k := range order[:min(5, len(order))] {
		f := byKey[k]
		fix := "no fix yet"
		if f.FixedIn != "" {
			fix = "fixed in " + f.FixedIn
		}
		kev := ""
		if f.KEV {
			kev = ", known exploited"
		}
		top = append(top, fmt.Sprintf("%s %s%s: %s %s in %s; %s", f.ID, f.Severity, kev, f.Package, f.Version, strings.Join(seen[k], ", "), fix))
	}
	sum := fmt.Sprintf("%d distinct vulnerabilities in %s. %s.", len(order), where, strings.Join(top, ". "))
	if len(r.Unscanned) > 0 {
		sum += fmt.Sprintf(" Not scanned yet: %s.", strings.Join(r.Unscanned[:min(3, len(r.Unscanned))], "; "))
	}
	return sum
}
