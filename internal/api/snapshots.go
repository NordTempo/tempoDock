package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// SnapshotPath addresses a snapshot of a service by ID.
type SnapshotPath struct {
	ServicePath
	Snapshot int64 `path:"snapshot" minimum:"1" doc:"Snapshot ID"`
}

// registerSnapshots registers volume snapshots (ADR 0047).
func registerSnapshots(s *Server, a huma.API) {
	tags := []string{"snapshots"}
	authed(s, a, huma.Operation{
		OperationID: "list-snapshots", Method: http.MethodGet, Path: servicePathT + "/snapshots",
		Summary: "List a service's snapshots", Tags: tags,
		Description: "A stateful service's volume snapshots, newest first, its latest restores, whether snapshots restore into a new service (template databases) and retention: the newest `keep` per service, none older than `keep_days`.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.ServiceSnapshots], error) {
		return reply(s.core.ServiceSnapshots(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-snapshot", Method: http.MethodPost, Path: servicePathT + "/snapshots",
		Summary: "Snapshot a service's volumes now", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Pauses the service's running containers and copies its volumes on its server, so the copy is of one moment; the containers resume when the copy is done. Snapshots stay on the server, count against its disk and are pruned by count and age. Runs in the background: the answer is the snapshot, running; snapshot.created or snapshot.failed is audited at the end. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.CreateSnapshotRequest
	}) (*out[apitypes.Snapshot], error) {
		return reply(s.core.CreateSnapshot(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "restore-snapshot", Method: http.MethodPost, Path: servicePathT + "/snapshots/{snapshot}/restore",
		Summary: "Restore a snapshot", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "A template database restores into a new service by default (`as`, default <service>-restored), and the service is not touched. `in_place` replaces the service's own volumes instead: it stops, a safety snapshot keeps the data it holds now (restore that one to undo), the snapshot is copied in and it starts again. Other services restore in place only. Runs in the background: the answer is the restore, running, and the new service; snapshot.restored or snapshot.restore_failed is audited at the end. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SnapshotPath
		Body apitypes.RestoreSnapshotRequest
	}) (*out[apitypes.SnapshotRestoreResult], error) {
		return reply(s.core.RestoreSnapshot(ctx, actor, in.ref(), in.Snapshot, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-snapshot-log", Method: http.MethodGet, Path: servicePathT + "/snapshots/{snapshot}/log",
		Summary: "Get a snapshot's log", Tags: tags,
		Description: "What a failed snapshot's job printed: its last 16 KB. Empty for a snapshot that succeeded. Scope: deploy, like a backup's log.",
	}, func(ctx context.Context, actor core.Actor, in *SnapshotPath) (*out[apitypes.BackupLog], error) {
		return reply(s.core.SnapshotLog(ctx, actor, in.ref(), in.Snapshot))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-snapshot", Method: http.MethodDelete, Path: servicePathT + "/snapshots/{snapshot}",
		Summary: "Delete a snapshot", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Deletes the snapshot's files on its server and its record. Refused while a backup, snapshot or restore of the service runs, or while the server is offline. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *SnapshotPath) (*struct{}, error) {
		return nil, s.core.DeleteSnapshot(ctx, actor, in.ref(), in.Snapshot)
	})
}
