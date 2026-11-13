package api

import (
	"context"
	"io/fs"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/docs/guide"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// registerGuide serves the user guide's pages, the same embedded files
// MCP gives agents (dock://docs/<page>), to the web UI's help. Public:
// the guide is documentation, the same for everyone.
func registerGuide(a huma.API) {
	public(a, huma.Operation{
		OperationID: "guide-page", Method: http.MethodGet, Path: Prefix + "/guide/{page}",
		Summary: "A page of the user guide, as Markdown", Tags: []string{"system"},
	}, func(_ context.Context, in *struct {
		Page string `path:"page" pattern:"^[A-Za-z0-9-]+$" example:"deploying"`
	}) (*out[apitypes.GuidePage], error) {
		b, err := fs.ReadFile(guide.Pages, in.Page+".md")
		if err != nil {
			return nil, huma.Error404NotFound("no guide page " + in.Page + ": the guide's index is README")
		}
		return reply(apitypes.GuidePage{Page: in.Page, Markdown: string(b)}, nil)
	})
}
