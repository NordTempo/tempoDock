//go:build integration

package core

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/s3"
)

// minioImage is an S3-compatible store for the test (MinIO as built by
// Pigsty; minio/minio is no longer published on Docker Hub).
const minioImage = "pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372"

// ADR 0039 with real restic and MinIO: a Postgres backup in the local
// repository moves into a bucket, is restored from its copy there, the
// local repository is empty afterwards, and the next backup goes to the
// bucket.
func TestIntegrationMoveBackups(t *testing.T) {
	var install string
	t.Cleanup(func() { removeInstallVolumes(t, install) })
	c := dockerHarness(t)
	install = c.install
	ctx := context.Background()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := startMinIOBucket(t, docker, "backups")

	project := fmt.Sprintf("mv-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, EnvRef{project, "production"}, apitypes.CreateServiceRequest{Name: "db", Template: apitypes.TemplatePostgres}); err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, db, 1); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db: %s %s", r.Status, r.Error)
	}
	for start := time.Now(); ; time.Sleep(200 * time.Millisecond) {
		if info, err := c.DataInfo(ctx, testSystem, db); err == nil && info.Status == apitypes.DataReady {
			break
		}
		if time.Since(start) > 30*time.Second {
			t.Fatal("data setup of the template database")
		}
	}
	execIn(t, c, db, "psql", "-U", "postgres", "-d", "app", "-v", "ON_ERROR_STOP=1", "-c",
		"CREATE TABLE notes (id int PRIMARY KEY); INSERT INTO notes SELECT generate_series(1, 300);")
	run, err := c.RunBackup(ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	local := waitBackupRun(t, c, db, run.ID)
	if local.Status != apitypes.BackupSucceeded || local.Repository != "local" {
		t.Fatalf("local backup: %+v", local)
	}

	if _, err := c.AddStorage(ctx, testSystem, apitypes.CreateStorageRequest{Name: "minio", Endpoint: endpoint, Bucket: "backups", AccessKeyID: "dockminio", SecretAccessKey: "dockminio-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MoveBackups(ctx, testSystem, apitypes.MoveBackupsRequest{Storage: "minio"}); err != nil {
		t.Fatal(err)
	}
	var set apitypes.BackupSettings
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(500 * time.Millisecond) {
		if set, err = c.GetBackupSettings(ctx, testSystem, false); err == nil && set.Move != nil && !set.Move.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("move still running: %+v", set.Move)
		}
	}
	if set.Move.Error != "" || set.Move.Moved != 1 || set.Storage != "minio" || set.Move.Sources[0].Status != "emptied" {
		t.Fatalf("move: %+v %+v", set.Move, set.Move.Sources)
	}

	// The moved backup restores from the bucket.
	b, err := c.ServiceBackups(ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	moved := b.Runs[0]
	if moved.Repository != set.Repository || moved.Snapshot == local.Snapshot {
		t.Fatalf("backup after the move: %+v", moved)
	}
	res, err := c.RestoreBackup(ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: moved.Snapshot[:8]})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitBackupRun(t, c, db, res.Restore.ID); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore from the bucket: %+v", r)
	}
	if out := execIn(t, c, ServiceRef{project, "production", "db-restored"}, "psql", "-U", "postgres", "-d", "app", "-tAc", "SELECT count(*) FROM notes"); out != "300" {
		t.Errorf("restored rows: %q", out)
	}

	// The local repository is empty, and new backups go to the bucket.
	if out := runInVolume(t, docker, c.localRepoVolume(), "ls -A /repo | wc -l"); out != "0" {
		t.Errorf("the local repository holds %s entries after the move", out)
	}
	run, err = c.RunBackup(ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	if next := waitBackupRun(t, c, db, run.ID); next.Status != apitypes.BackupSucceeded || next.Repository != set.Repository {
		t.Fatalf("backup after the move: %+v", next)
	}
}

// startMinIOBucket runs MinIO with one bucket and returns its endpoint,
// published on the Docker bridge's gateway so the test and job
// containers reach it at the same address.
func startMinIOBucket(t *testing.T, docker *client.Client, bucket string) string {
	t.Helper()
	ctx := t.Context()
	if _, err := docker.ImageInspect(ctx, minioImage); err != nil {
		pull, err := docker.ImagePull(ctx, minioImage, client.ImagePullOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := pull.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	br, err := docker.NetworkInspect(ctx, "bridge", client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gw := br.Network.IPAM.Config[0].Gateway
	port := network.MustParsePort("9000/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: minioImage, Cmd: []string{"minio", "server", "/data"},
			Env:          []string{"MINIO_ROOT_USER=dockminio", "MINIO_ROOT_PASSWORD=dockminio-secret"},
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{port: {{HostIP: gw}}}, Tmpfs: map[string]string{"/data": ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspect, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + net.JoinHostPort(gw.String(), inspect.Container.NetworkSettings.Ports[port][0].HostPort)
	mk := &s3.Client{Bucket: s3.Bucket{Endpoint: endpoint, Name: bucket, PathStyle: true, AccessKeyID: "dockminio", SecretAccessKey: "dockminio-secret"}}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		err := mk.Put(ctx, "", nil, 0) // PUT on the bucket creates it
		if err == nil {
			return endpoint
		}
		if time.Now().After(deadline) {
			t.Fatalf("create bucket: %v", err)
		}
	}
}

// runInVolume runs a shell command in a throwaway container with volume
// mounted at /repo and returns its trimmed output.
func runInVolume(t *testing.T, docker *client.Client, volume, cmd string) string {
	t.Helper()
	ctx := t.Context()
	const img = "busybox:latest"
	if _, err := docker.ImageInspect(ctx, img); err != nil {
		pull, err := docker.ImagePull(ctx, img, client.ImagePullOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := pull.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: img, Cmd: []string{"sh", "-c", cmd}},
		HostConfig: &container.HostConfig{Binds: []string{volume + ":/repo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-wait.Result:
	case err := <-wait.Error:
		t.Fatal(err)
	}
	logs, err := docker.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logs.Close() }()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := logs.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	// Docker multiplexes stdout with 8-byte frame headers; keep the digits.
	return strings.TrimFunc(sb.String(), func(r rune) bool { return r < '0' || r > '9' })
}
