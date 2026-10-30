package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerRegistry registers the platform registry's console endpoints.
func registerRegistry(s *Server, a huma.API) {
	tags := []string{"registry"}
	authed(s, a, huma.Operation{
		OperationID: "get-registry", Method: http.MethodGet, Path: Prefix + "/registry",
		Summary: "Describe the registry", Tags: tags,
		Description: "The platform registry's size on disk, how pruning went since the daemon started, and the services keeping images in it. enabled is false on installs without a registry. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Registry], error) {
		return reply(s.core.GetRegistry(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "prune-registry", Method: http.MethodPost, Path: Prefix + "/registry/prune",
		Summary: "Prune the registry now", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Deletes the images no release keeps (outside every service's rollback window and not active) and collects their layers, in the background; the registry stops for those seconds. Answers with the registry, pruning. 409 while builds or pulls use the registry or a prune runs. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.Registry], error) {
		return reply(s.core.PruneRegistry(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "migrate-registry", Method: http.MethodPost, Path: Prefix + "/registry/migrate",
		Summary: "Move the registry's images to other storage", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Copies the registry's images to the control plane's disk (to local) or a storage connection's bucket (to s3) in the background, then switches the registry over and deletes the old copy (ADR 0034). The registry serves them read-only meanwhile, and builds and pulls wait. Answers with the registry, its migration running; registry.migrated or registry.migrate_failed is audited at the end. 409 while builds or pulls use the registry, or a prune or migration runs. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.MigrateRegistryRequest
	}) (*out[apitypes.Registry], error) {
		return reply(s.core.MigrateRegistry(ctx, actor, in.Body))
	})
}

// StoragePath names a storage connection.
type StoragePath struct {
	Storage string `path:"storage" doc:"Storage connection name"`
}

// registerStorage registers the storage connections' endpoints (ADR 0034).
func registerStorage(s *Server, a huma.API) {
	tags := []string{"storage"}
	const one = Prefix + "/storage/{storage}"
	authed(s, a, huma.Operation{
		OperationID: "list-storage", Method: http.MethodGet, Path: Prefix + "/storage",
		Summary: "List storage connections", Tags: tags,
		Description: "The S3-compatible buckets defined for Dock, and what keeps data in each. Keys are never returned. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.Storage], error) {
		return reply(s.core.ListStorage(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-storage", Method: http.MethodPost, Path: Prefix + "/storage",
		Summary: "Add a storage connection", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "An S3-compatible bucket: endpoint, region, bucket, keys and addressing. Dock lists the bucket with the keys before saving it, so a mistake fails here (400, saying what to check). Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.CreateStorageRequest
	}) (*out[apitypes.Storage], error) {
		return reply(s.core.AddStorage(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-storage", Method: http.MethodPatch, Path: one,
		Summary: "Change a storage connection", Tags: tags,
		Description: "Fields left out stay as they are; name renames it, and what uses it follows. The bucket is listed again unless only the name changed. While the registry keeps its images in it, the endpoint and bucket can't change (409), and new keys restart the registry. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		StoragePath
		Body apitypes.UpdateStorageRequest
	}) (*out[apitypes.Storage], error) {
		return reply(s.core.UpdateStorage(ctx, actor, in.Storage, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-storage", Method: http.MethodDelete, Path: one,
		Summary: "Remove a storage connection", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "The bucket and its contents are left alone. 409 while the registry keeps its images in it. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *StoragePath) (*struct{}, error) {
		return nil, s.core.RemoveStorage(ctx, actor, in.Storage)
	})
}
