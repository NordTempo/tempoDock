package api

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerScans adds image vulnerability scanning (ADR 0074): the org's
// images, one image, a service's live image, its SBOM, a rescan, and the
// operators' switch.
func registerScans(s *Server, a huma.API) {
	orgPath := Prefix + "/org"
	tags := []string{"images"}
	authed(s, a, huma.Operation{
		OperationID: "list-images", Method: http.MethodGet, Path: orgPath + "/images",
		Summary: "List images in use", Tags: tags,
		Description: "Every image the organization's live releases run, one row per image however many services run it, worst first, with its vulnerability scan. Scans run on builders once no build or deploy has run for a minute. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Production bool `query:"production" doc:"Only images in production environments"`
	}) (*out[apitypes.ImageList], error) {
		return reply(s.core.ListImages(ctx, actor, in.Production))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-image", Method: http.MethodGet, Path: orgPath + "/images/{id}",
		Summary: "Get an image", Tags: tags,
		Description: "One image: the releases that run it, its scan, its findings grouped by what fixes them, and all of them worst first. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id" minimum:"1"`
	}) (*out[apitypes.ImageDetail], error) {
		return reply(s.core.GetImage(ctx, actor, in.ID))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-vulnerabilities", Method: http.MethodGet, Path: orgPath + "/vulnerabilities",
		Summary: "List vulnerabilities in live releases", Tags: tags,
		Description: "Findings at or above a severity in the organization's live releases, worst first, with where they run and the version that fixes them, and the live services not scanned yet. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Production bool                  `query:"production" doc:"Only production environments"`
		Min        apitypes.VulnSeverity `query:"min" default:"high" enum:"critical,high,medium,low,negligible,unknown"`
	}) (*out[apitypes.OrgFindings], error) {
		return reply(s.core.OrgFindings(ctx, actor, in.Production, in.Min))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-service-vulnerabilities", Method: http.MethodGet, Path: servicePathT + "/vulnerabilities",
		Summary: "Get a service's vulnerabilities", Tags: tags,
		Description: "The scan of the image the service's live release (or release N) runs, with its findings. 404 until it is scanned. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Release int64 `query:"release" minimum:"0" doc:"Release number; default the live one"`
	}) (*out[apitypes.ImageDetail], error) {
		return reply(s.core.ServiceVulnerabilities(ctx, actor, in.ref(), in.Release))
	})
	authed(s, a, huma.Operation{
		OperationID: "scan-service", Method: http.MethodPost, Path: servicePathT + "/scan",
		Summary: "Scan a service's image again", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Queues the image of the live release (or release N) for a scan: against the current vulnerability database, from its SBOM when it has one. It still waits until no build or deploy has run for a minute. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.ScanRequest
	}) (*out[apitypes.ImageScan], error) {
		return reply(s.core.ScanService(ctx, actor, in.ref(), in.Body.Release))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-service-sbom", Method: http.MethodGet, Path: servicePathT + "/sbom",
		Summary: "Download a service's SBOM", Tags: tags,
		Description: "The software bill of materials of the live release's image (or release N's) as Syft made it: syft-json, gzipped. Scope: read.",
		Responses: map[string]*huma.Response{"200": {
			Description: "SBOM",
			Content:     map[string]*huma.MediaType{"application/gzip": {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}},
		}},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Release int64 `query:"release" minimum:"0" doc:"Release number; default the live one"`
	}) (*huma.StreamResponse, error) {
		f, name, err := s.core.OpenSBOM(ctx, actor, in.ref(), in.Release)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			defer func() { _ = f.Close() }()
			hctx.SetHeader("Content-Type", "application/gzip")
			hctx.SetHeader("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
			hctx.SetStatus(http.StatusOK)
			_, _ = io.Copy(hctx.BodyWriter(), f)
		}}, nil
	})

	tags = []string{"console"}
	authed(s, a, huma.Operation{
		OperationID: "get-scanning", Method: http.MethodGet, Path: Prefix + "/scanning",
		Summary: "Describe image scanning", Tags: tags,
		Description: "Whether image scanning is on, scans by state, and each builder's vulnerability database. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.ScanningStatus], error) {
		return reply(s.core.ScanningStatus(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-scanning", Method: http.MethodPut, Path: Prefix + "/scanning",
		Summary: "Turn image scanning on or off", Tags: tags,
		Description: "Off stops a running scan at once and starts none; findings already made stay. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.ScanningSettings
	}) (*out[apitypes.ScanningStatus], error) {
		return reply(s.core.SetScanning(ctx, actor, in.Body.Enabled))
	})
}
