package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/s3"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Backups on storage connections (ADR 0039). Backups go to a local
// repository on each server, or to a folder in a storage connection's
// bucket. Moving them copies every source repository into the bucket with
// restic, checks the copy, points each backup at its copied snapshot (new
// IDs; restic records the original), switches the setting and empties the
// sources. No backup or restore starts while it runs.

// defaultBackupPath is the folder in the bucket backups go to.
const defaultBackupPath = "dock-backups"

// moveSource is one repository a move copies from, and where its job runs.
type moveSource struct {
	// label is how backups record the repository: "" (local) or its URL.
	label    string
	serverID int64
	server   string
	from     backup.Repo
	// prefix is the source's folder in its bucket, emptied with the S3
	// client after the move; "" for a local repository.
	bucket *s3.Bucket
	prefix string
}

// MoveBackups moves the backups into a folder (req.Path, default
// dock-backups) of a storage connection's bucket and makes it where new
// backups go. It answers with the settings, the move running, and works
// in the background; backup.moved or backup.move_failed is audited at the
// end. A failure before the switch changes nothing but a partial copy in
// the bucket, which a new move continues. Refused while backups or
// restores run. From a bucket it needs the same endpoint and keys (restic
// copies with one set). Operators only.
func (c *Core) MoveBackups(ctx context.Context, a Actor, req apitypes.MoveBackupsRequest) (apitypes.BackupSettings, error) {
	if err := a.requireOperator("moving backups"); err != nil {
		return apitypes.BackupSettings{}, err
	}
	path := strings.Trim(strings.TrimSpace(req.Path), "/")
	if path == "" {
		path = defaultBackupPath
	}
	if err := backup.ValidPath(path); err != nil {
		return apitypes.BackupSettings{}, invalid("%v", err)
	}
	conn, err := c.store.GetStorageConnection(ctx, req.Storage)
	if store.IsNotFound(err) {
		return apitypes.BackupSettings{}, notFound("no storage connection named %s; add it with `dock storage add`", req.Storage)
	}
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	b, err := c.bucketOf(conn)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	targetURL := backup.S3URL(conn.Endpoint, conn.Bucket, path)
	if targetURL == set.Repository {
		return apitypes.BackupSettings{}, invalid("backups go to %s already", targetURL)
	}
	cur, err := c.repoOf(ctx, set) // creates the password on first use
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	target := backup.Repo{URL: targetURL, AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey, Region: b.Region, PathStyle: b.PathStyle, Password: cur.Password}
	sources, err := c.moveSources(ctx, set, cur, b, path)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}

	st := &c.backups
	st.mu.Lock()
	switch {
	case st.moving():
		st.mu.Unlock()
		return apitypes.BackupSettings{}, conflict("backups are moving already; wait for that to finish")
	case len(st.running) > 0 || st.controlPlane:
		st.mu.Unlock()
		return apitypes.BackupSettings{}, conflict("backups or restores are running; move once they finish")
	}
	move := &apitypes.BackupMove{To: conn.Name, Path: path, Running: true, StartedAt: now().UTC().Truncate(time.Second), Sources: []apitypes.BackupMoveSource{}}
	for _, s := range sources {
		// A bucket is one repository, whichever server copies it.
		server := s.server
		if s.label != "" {
			server = ""
		}
		move.Sources = append(move.Sources, apitypes.BackupMoveSource{Repository: repoLabel(s.label), Server: server, Status: "waiting"})
	}
	st.move = move
	st.moveTo = conn.ID
	st.mu.Unlock()
	c.audit(ctx, a.platform(), "backup.move", nil, nil, "backups", fmt.Sprintf("started moving the backups from %s to %s", repoLabel(set.Repository), targetURL))
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.runMove(c.ctx, a, sources, target, conn.ID, path)
	}()
	return c.GetBackupSettings(ctx, a, false)
}

// moveSources lists the repositories a move copies from: each server's
// local repository that holds backups restores can use (and the control
// plane's, for its own backup), or the current bucket's repository.
func (c *Core) moveSources(ctx context.Context, set store.BackupSetting, cur backup.Repo, to s3.Bucket, toPath string) ([]moveSource, error) {
	if set.Repository != "" {
		if set.StorageID == nil {
			return nil, invalid("backups go to %s, set before storage connections; restart the daemon so it becomes one, then move", set.Repository)
		}
		conn, err := c.storageByID(ctx, *set.StorageID)
		if err != nil {
			return nil, err
		}
		from, err := c.bucketOf(conn)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(from.Endpoint, to.Endpoint) || from.AccessKeyID != to.AccessKeyID || from.PathStyle != to.PathStyle {
			return nil, invalid("moving backups between buckets needs the same endpoint, keys and addressing: restic copies with one set of keys")
		}
		// Emptying the source deletes its folder: it must not hold the
		// target, nor the target it.
		if from.Name == to.Name && nested(set.StoragePath, toPath) {
			return nil, invalid("folder %q and %q in bucket %s contain one another; pick a folder outside the current one", set.StoragePath, toPath, to.Name)
		}
		return []moveSource{{label: set.Repository, serverID: c.local.ID, server: c.local.Name, from: cur, bucket: &from, prefix: set.StoragePath}}, nil
	}
	ids, err := c.store.ListLocalBackupServers(ctx)
	if err != nil {
		return nil, err
	}
	if cp, err := c.store.GetControlPlaneBackup(ctx); err == nil && cp.Repository == "" && cp.SnapshotID != "" {
		ids = append(ids, c.local.ID)
	} else if err != nil && !store.IsNotFound(err) {
		return nil, err
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	out := make([]moveSource, 0, len(ids))
	for _, id := range ids {
		srv, err := c.store.GetServer(ctx, id)
		if err != nil {
			return nil, err
		}
		if c.connected(id) == nil {
			return nil, errf(apitypes.CodeServerOffline, "server %s holds backups and is offline; move once it is back", srv.Name)
		}
		out = append(out, moveSource{label: "", serverID: id, server: srv.Name, from: backup.Repo{Password: cur.Password}})
	}
	return out, nil
}

// nested reports whether folders a and b of one bucket contain one
// another; the bucket's root ("") contains every folder.
func nested(a, b string) bool {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	if a == "" || b == "" {
		return true
	}
	return strings.HasPrefix(a+"/", b+"/") || strings.HasPrefix(b+"/", a+"/")
}

// runMove runs a move MoveBackups started and records how it went.
func (c *Core) runMove(ctx context.Context, a Actor, sources []moveSource, target backup.Repo, connID int64, path string) {
	moved, gone, cleanup, err := c.move(ctx, sources, target, connID, path)
	st := &c.backups
	st.mu.Lock()
	m := st.move
	m.Running, m.Moved, m.Gone = false, moved, gone
	t := now().UTC().Truncate(time.Second)
	m.FinishedAt = &t
	if err != nil {
		m.Error = err.Error()
	}
	st.mu.Unlock()
	c.kickBackups()
	if err != nil {
		c.log.Warn("backups: move failed", "to", target.URL, "err", err)
		c.audit(ctx, System, "backup.move_failed", nil, nil, "backups", fmt.Sprintf("moving the backups to %s failed, they stay where they were: %v", target.URL, err))
		return
	}
	summary := fmt.Sprintf("moved %d backups to %s", moved, target.URL)
	if gone > 0 {
		summary += fmt.Sprintf("; %d were gone from their repository already and are marked pruned", gone)
	}
	if cleanup != nil {
		summary += fmt.Sprintf("; %v", cleanup)
	}
	c.log.Info("backups: moved", "to", target.URL, "backups", moved)
	c.audit(ctx, System, "backup.moved", nil, nil, "backups", summary)
}

// move copies each source into target, checks and lists target, points
// the backups at their copies and switches the setting in one
// transaction, then empties the sources. Backups are matched to their
// copies through restic's "original", read from each source's listing
// (the copy job's) and the target's (the check's): a backup's snapshot
// may itself be a copy from an earlier move. A backup whose snapshot is
// in no source any more was gone before the move; it is marked pruned
// (gone counts them). err means nothing switched; cleanup says which
// sources couldn't be emptied.
func (c *Core) move(ctx context.Context, sources []moveSource, target backup.Repo, connID int64, path string) (moved, gone int, cleanup, err error) {
	// origins maps each source repository's snapshots to what they are
	// copies of, by repository label ("" is every local repository).
	origins := map[string]map[string]string{}
	for i, src := range sources {
		c.setMoveStatus(i, "copying", "")
		rt := c.connected(src.serverID)
		if rt == nil {
			err := fmt.Errorf("server %s went offline", src.server)
			c.setMoveStatus(i, "failed", err.Error())
			return 0, 0, nil, err
		}
		spec := node.JobSpec{
			Name: fmt.Sprintf("dock_backup_move_%d", now().UnixNano()), Image: c.cfg.Backup.ResticImage,
			Labels: node.Labels{ControlPlane: c.install}, Script: backup.CopyScript, Env: backup.CopyEnv(src.from, target),
		}
		out, err := c.runResticJob(ctx, src.serverID, rt, src.from, spec)
		var listed map[string]string
		if err == nil {
			listed, err = backup.ParseMoved(out)
		}
		if err != nil {
			err = fmt.Errorf("copy from %s on %s: %w", repoLabel(src.label), src.server, err)
			c.setMoveStatus(i, "failed", err.Error())
			return 0, 0, nil, err
		}
		if origins[src.label] == nil {
			origins[src.label] = map[string]string{}
		}
		maps.Copy(origins[src.label], listed)
		c.setMoveStatus(i, "copied", "")
		// Each copy's end is audited, so the Backups page refetches as the
		// move goes on (data/live.ts).
		c.audit(ctx, System, "backup.move_copied", nil, nil, "backups", fmt.Sprintf("copied %s on %s to %s", repoLabel(src.label), src.server, target.URL))
	}
	// Check the target and list what it holds, on the control plane.
	// With nothing copied there is nothing to check: the first backup
	// creates the repository, as it always has.
	copies := map[string]string{} // original -> ID in the target
	if len(sources) > 0 {
		out, err := c.runResticJob(ctx, c.local.ID, c.rt, target, node.JobSpec{
			Name: fmt.Sprintf("dock_backup_move_check_%d", now().UnixNano()), Image: c.cfg.Backup.ResticImage,
			Labels: node.Labels{ControlPlane: c.install}, Script: backup.CheckScript, Env: target.Env(),
		})
		var listed map[string]string
		if err == nil {
			listed, err = backup.ParseMoved(out)
		}
		if err != nil {
			return 0, 0, nil, fmt.Errorf("check %s: %w", target.URL, err)
		}
		for id, orig := range listed {
			copies[orig] = id
		}
	}

	// Every backup whose snapshot a source holds must have its copy.
	type repoint struct{ from, oldID, newID string }
	var points, lost []repoint
	var missing []string
	find := func(label, id string) (newID string, held, ok bool) {
		orig, held := origins[label][id]
		if !held {
			return "", false, false
		}
		newID, ok = copies[orig]
		return newID, true, ok
	}
	for label := range origins {
		ids, err := c.store.ListRepositorySnapshots(ctx, label)
		if err != nil {
			return 0, 0, nil, err
		}
		for _, id := range ids {
			switch n, held, ok := find(label, id); {
			case ok:
				points = append(points, repoint{label, id, n})
			case held:
				missing = append(missing, id[:min(8, len(id))])
			default:
				lost = append(lost, repoint{from: label, oldID: id})
			}
		}
	}
	cp, err := c.store.GetControlPlaneBackup(ctx)
	if err != nil && !store.IsNotFound(err) {
		return 0, 0, nil, err
	}
	cpCopy, cpMoves := "", false
	if err == nil && origins[cp.Repository] != nil && cp.SnapshotID != "" {
		var held bool
		cpCopy, held, cpMoves = find(cp.Repository, cp.SnapshotID)
		if held && !cpMoves {
			missing = append(missing, cp.SnapshotID[:min(8, len(cp.SnapshotID))]+" (control plane)")
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return 0, 0, nil, fmt.Errorf("%d backups have no copy in %s (%s); nothing was switched", len(missing), target.URL, strings.Join(missing[:min(5, len(missing))], ", "))
	}

	err = c.store.Tx(ctx, func(q *store.Queries) error {
		for _, p := range points {
			if err := q.MoveBackupSnapshot(ctx, store.MoveBackupSnapshotParams{Repository: target.URL, SnapshotID: p.newID, Repository_2: p.from, SnapshotID_2: p.oldID}); err != nil {
				return err
			}
		}
		for _, p := range lost {
			if err := q.PruneBackupSnapshot(ctx, store.PruneBackupSnapshotParams{Repository: p.from, SnapshotID: p.oldID}); err != nil {
				return err
			}
		}
		if cpMoves {
			if err := q.MoveControlPlaneSnapshot(ctx, store.MoveControlPlaneSnapshotParams{Repository: target.URL, SnapshotID: cpCopy, Repository_2: cp.Repository, SnapshotID_2: cp.SnapshotID}); err != nil {
				return err
			}
		}
		set, err := q.GetBackupSettings(ctx)
		if store.IsNotFound(err) {
			set = store.BackupSetting{ID: 1, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6, Hour: 3}
		} else if err != nil {
			return err
		}
		// The password stays: MoveBackups had repoOf create it.
		set.StorageID, set.StoragePath, set.UpdatedAt = &connID, path, now()
		_, err = q.UpsertBackupSettings(ctx, upsertParams(set))
		return err
	})
	if err != nil {
		return 0, 0, nil, fmt.Errorf("point the backups at their copies: %w", err)
	}
	moved, gone = len(points), len(lost)
	if cpMoves {
		moved++
	}

	// Empty the sources: their backups live in the bucket now.
	var failed []error
	for i, src := range sources {
		if err := c.emptySource(ctx, src); err != nil {
			c.setMoveStatus(i, "copied", "not emptied: "+err.Error())
			failed = append(failed, fmt.Errorf("%s on %s wasn't emptied (%w); delete it by hand", repoLabel(src.label), src.server, err))
			continue
		}
		c.setMoveStatus(i, "emptied", "")
	}
	return moved, gone, errors.Join(failed...), nil
}

// emptySource deletes a source repository's contents after a move: the
// files of a local repository (its volume stays), or the objects under a
// bucket repository's folder. A repository at a bucket's root isn't
// deleted: that would empty the whole bucket.
func (c *Core) emptySource(ctx context.Context, src moveSource) error {
	if src.bucket != nil {
		if src.prefix == "" {
			return errors.New("it is the bucket's root; Dock only deletes a folder")
		}
		cl := &s3.Client{Bucket: *src.bucket}
		return cl.List(ctx, src.prefix+"/", func(page []s3.Object) error {
			keys := make([]string, len(page))
			for i, o := range page {
				keys[i] = o.Key
			}
			return cl.Delete(ctx, keys)
		})
	}
	rt := c.connected(src.serverID)
	if rt == nil {
		return fmt.Errorf("server %s went offline", src.server)
	}
	_, err := c.runResticJob(ctx, src.serverID, rt, src.from, node.JobSpec{
		Name: fmt.Sprintf("dock_backup_move_empty_%d", now().UnixNano()), Image: c.cfg.Backup.ResticImage,
		Labels: node.Labels{ControlPlane: c.install}, Script: backup.ClearLocalScript,
	})
	return err
}

// setMoveStatus records how source i of the running move is doing.
func (c *Core) setMoveStatus(i int, status, msg string) {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	if m := c.backups.move; m != nil && i < len(m.Sources) {
		m.Sources[i].Status, m.Sources[i].Error = status, msg
	}
}

// backupsMovingTo reports whether a running backup move copies into or
// out of storage connection id.
func (c *Core) backupsMovingTo(id int64) bool {
	c.backups.mu.Lock()
	moving, to := c.backups.moving(), c.backups.moveTo
	c.backups.mu.Unlock()
	if !moving {
		return false
	}
	if to == id {
		return true
	}
	set, err := c.store.GetBackupSettings(c.ctx)
	return err == nil && set.StorageID != nil && *set.StorageID == id
}

// backupRepositories lists where the backups restores can use are: each
// local repository by server, and each bucket's repository, with the
// control plane's latest backup counted on its server. The repository
// new backups go to is listed even while empty.
func (c *Core) backupRepositories(ctx context.Context, current string) ([]apitypes.BackupRepository, error) {
	rows, err := c.store.ListBackupRepositories(ctx)
	if err != nil {
		return nil, err
	}
	type key struct{ repo, server string }
	sums := map[key]*apitypes.BackupRepository{}
	add := func(repo, server string, n int, size int64) {
		if repo != "" {
			server = "" // a bucket is one repository, whichever server backed up
		}
		k := key{repo, server}
		r := sums[k]
		if r == nil {
			r = &apitypes.BackupRepository{Repository: repoLabel(repo), Server: server, Current: repo == current}
			sums[k] = r
		}
		r.Snapshots += n
		r.Bytes += size
	}
	for _, r := range rows {
		add(r.Repository, r.Server, int(r.Snapshots), r.SizeBytes)
	}
	cp, err := c.store.GetControlPlaneBackup(ctx)
	if err == nil && cp.Status == string(apitypes.BackupSucceeded) && cp.SnapshotID != "" {
		add(cp.Repository, c.local.Name, 1, cp.SizeBytes)
	} else if err != nil && !store.IsNotFound(err) {
		return nil, err
	}
	if current != "" {
		add(current, "", 0, 0)
	}
	out := make([]apitypes.BackupRepository, 0, len(sums))
	for _, r := range sums {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b apitypes.BackupRepository) int {
		return cmp.Or(cmp.Compare(a.Repository, b.Repository), cmp.Compare(a.Server, b.Server))
	})
	return out, nil
}

// connectionName turns a bucket's name into a storage connection's.
var connectionName = regexp.MustCompile(`[^a-z0-9-]+`)

// convertBackupRepository turns a repository URL and keys set with `dock
// backup configure` before storage connections into a connection named
// after the bucket, on start. The URL in the backups' history is
// relabelled if the connection writes it differently.
func (c *Core) convertBackupRepository(ctx context.Context) error {
	s, err := c.store.GetBackupSettings(ctx)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil || s.StorageID != nil || s.Repository == "" {
		return err
	}
	endpoint, bucket, path, err := backup.ParseS3URL(s.Repository)
	if err != nil {
		return err
	}
	var keys s3Credentials
	if s.Credentials != nil {
		plain, err := c.box.Open(s.Credentials)
		if err != nil {
			return fmt.Errorf("open the backup credentials: %w", err)
		}
		if err := json.Unmarshal(plain, &keys); err != nil {
			return err
		}
	}
	sealed, err := c.sealKeys(s3.Bucket{AccessKeyID: keys.AccessKeyID, SecretAccessKey: keys.SecretAccessKey})
	if err != nil {
		return err
	}
	base := strings.Trim(connectionName.ReplaceAllString(strings.ToLower(bucket), "-"), "-")
	base = cmp.Or(base[:min(len(base), 24)], "backups")
	name := base
	for i := 2; ; i++ {
		if _, err := c.store.GetStorageConnection(ctx, name); store.IsNotFound(err) {
			break
		} else if err != nil {
			return err
		}
		name = base + "-" + strconv.Itoa(i)
	}
	url := backup.S3URL(endpoint, bucket, path)
	t := now()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		conn, err := q.CreateStorageConnection(ctx, store.CreateStorageConnectionParams{
			Name: name, Endpoint: endpoint, Region: keys.Region, Bucket: bucket,
			// restic's default for endpoints other than AWS.
			PathStyle:   !strings.Contains(endpoint, "amazonaws.com"),
			Credentials: sealed, CreatedAt: t, UpdatedAt: t, UpdatedBy: "system",
		})
		if err != nil {
			return err
		}
		old := s.Repository
		s.StorageID, s.StoragePath = &conn.ID, path
		if _, err := q.UpsertBackupSettings(ctx, upsertParams(s)); err != nil {
			return err
		}
		if url != old {
			if err := q.RelabelBackupRepository(ctx, store.RelabelBackupRepositoryParams{Repository: url, Repository_2: old}); err != nil {
				return err
			}
			return q.RelabelControlPlaneRepository(ctx, store.RelabelControlPlaneRepositoryParams{Repository: url, Repository_2: old})
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.audit(ctx, System, "backup.configure", nil, nil, "backups", fmt.Sprintf("the backup repository %s became storage connection %s", url, name))
	return nil
}
