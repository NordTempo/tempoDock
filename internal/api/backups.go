package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerBackups registers database templates and backups (ADR 0019).
func registerBackups(s *Server, a huma.API) {
	tags := []string{"backups"}
	authed(s, a, huma.Operation{
		OperationID: "list-templates", Method: http.MethodGet, Path: Prefix + "/templates",
		Summary: "List database templates", Tags: tags,
		Description: "Create one with `template` in create-service.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[[]apitypes.TemplateInfo], error) {
		return reply(s.core.ListTemplates(ctx, actor), nil)
	})
	authed(s, a, huma.Operation{
		OperationID: "get-backup-settings", Method: http.MethodGet, Path: Prefix + "/backup",
		Summary: "Backup settings", Tags: tags,
		Description: "The platform's repository, retention and schedule. `reveal` adds the repository password (operators).",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Reveal bool `query:"reveal" doc:"Include the repository password"`
	}) (*out[apitypes.BackupSettings], error) {
		return reply(s.core.GetBackupSettings(ctx, actor, in.Reveal))
	})
	authed(s, a, huma.Operation{
		OperationID: "configure-backups", Method: http.MethodPut, Path: Prefix + "/backup",
		Summary: "Configure backups", Tags: tags,
		Description: "Retention and the hour; fields left out stay as they are. Where backups go is a storage connection, set by moving them (POST /backup/move); repository and the S3 keys are refused. The answer carries the repository password when this call generated it; store it safely. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.ConfigureBackupRequest
	}) (*out[apitypes.BackupSettings], error) {
		return reply(s.core.ConfigureBackups(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "move-backups", Method: http.MethodPost, Path: Prefix + "/backup/move",
		Summary: "Move the backups to a storage connection", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Copies every repository holding backups (each server's local one, or the current bucket's) into a folder of the connection's bucket with restic, checks the copy, points each backup at its copy so it stays restorable, makes the bucket where new backups go, and empties the sources (ADR 0039). Runs in the background: the answer is the settings with the move running; backup.moved or backup.move_failed is audited at the end. Backups and restores started meanwhile are refused (409); scheduled ones wait. A failure before the switch changes nothing. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.MoveBackupsRequest
	}) (*out[apitypes.BackupSettings], error) {
		return reply(s.core.MoveBackups(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "run-control-plane-backup", Method: http.MethodPost, Path: Prefix + "/backup/control-plane",
		Summary: "Back up the control plane now", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Starts a backup of the control plane (database, keys, config) into the repository in the background and answers with it, running. It also runs daily at the install's hour. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.ControlPlaneBackupRun], error) {
		return reply(s.core.RunControlPlaneBackup(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-backups", Method: http.MethodGet, Path: servicePathT + "/backups",
		Summary: "List a service's backups", Tags: tags,
		Description: "How and when a stateful service is backed up, and its latest backups and restores.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.ServiceBackups], error) {
		return reply(s.core.ServiceBackups(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-backup-log", Method: http.MethodGet, Path: servicePathT + "/backups/{id}/log",
		Summary: "Get a backup's log", Tags: tags,
		Description: "What a backup or restore run's job printed: its last 16 KB, with the repository's and database's secrets masked. Kept for the service's newest 20 runs; empty when none was kept. Scope: deploy, as a database's output can show its data.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		ID int64 `path:"id"`
	}) (*out[apitypes.BackupLog], error) {
		return reply(s.core.BackupLog(ctx, actor, in.ref(), in.ID))
	})
	authed(s, a, huma.Operation{
		OperationID: "run-backup", Method: http.MethodPost, Path: servicePathT + "/backups",
		Summary: "Back up a service now", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Starts a backup in the background and answers with it, running. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.BackupRun], error) {
		return reply(s.core.RunBackup(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-backup-schedule", Method: http.MethodPut, Path: servicePathT + "/backups/schedule",
		Summary: "Change a service's backup schedule", Tags: tags,
		Description: "Turn daily backups on or off, or set their hour. Backups are the platform's: operators only (with read on the service).",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.UpdateBackupScheduleRequest
	}) (*out[apitypes.ServiceBackups], error) {
		return reply(s.core.UpdateBackupSchedule(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "restore-backup", Method: http.MethodPost, Path: servicePathT + "/backups/restore",
		Summary: "Restore a backup into a new service", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Creates a new service from the template with the snapshot's data; the live service is never touched. Answers with the new service at once; the restore finishes in the background. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.RestoreBackupRequest
	}) (*out[apitypes.RestoreResult], error) {
		return reply(s.core.RestoreBackup(ctx, actor, in.ref(), in.Body))
	})
}
