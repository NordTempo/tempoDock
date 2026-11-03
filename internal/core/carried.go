package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The values in effect are those of the active release. Applying staged
// changes makes them live in the DB before the releases that use them have
// passed their health checks, so each release records the changes it
// carried (releases.carried). If it fails, they are staged again: the live
// values go back to what the active release runs with, `dock changes` shows
// them with the failure, and the next unrelated deploy doesn't ship them.
//
// A release carries its own service's changes and the environment-level
// (shared) variable changes applied with it. Services redeployed only
// because they reference a changed variable carry nothing of their own.
// A shared variable goes back to staged if any release carrying it fails,
// even if other services already run with it; the staged change shows it.

// carried is the JSON in releases.carried.
type carried struct {
	Variables []carriedVar     `json:"variables,omitempty"`
	Settings  *carriedSettings `json:"settings,omitempty"`
	// Volumes are the IDs of volumes the release mounted for the first time.
	Volumes []int64 `json:"volumes,omitempty"`
}

// carriedVar is one applied variable change. Values are sealed exactly as
// stored, so "still live" is a byte comparison.
type carriedVar struct {
	ServiceID *int64 `json:"service_id,omitempty"` // nil: shared
	Key       string `json:"key"`
	Old       []byte `json:"old,omitempty"` // live value before; nil: added
	New       []byte `json:"new,omitempty"` // value applied; nil: unset
}

// carriedSettings are the live settings before (Old) and after (New) the
// release. A failure puts Old back and stages Staged again, or New when
// Staged is nil.
type carriedSettings struct {
	Old apitypes.ServiceSettings `json:"old"`
	New apitypes.ServiceSettings `json:"new"`
	// Staged is set when the deploy itself changed the image setting: the
	// image it asked for isn't staged again, only what was staged.
	Staged *apitypes.ServiceSettings `json:"staged,omitempty"`
}

// withImage records that a deploy changed the live settings from before
// (staged settings already applied) to after by setting the image. The
// release then carries the image change, so a failure restores the image
// along with the staged settings, which are staged again without it.
func (c carried) withImage(before, after apitypes.ServiceSettings) carried {
	if before == after {
		return c
	}
	old := before
	if c.Settings != nil {
		old = c.Settings.Old
	}
	c.Settings = &carriedSettings{Old: old, New: after, Staged: &before}
	return c
}

func (c carried) empty() bool {
	return len(c.Variables) == 0 && c.Settings == nil && len(c.Volumes) == 0
}

// encode renders c for releases.carried ("" when empty).
func (c carried) encode() (string, error) {
	if c.empty() {
		return "", nil
	}
	b, err := json.Marshal(c)
	return string(b), err
}

func decodeCarried(s string) (carried, error) {
	var c carried
	if s == "" {
		return c, nil
	}
	return c, json.Unmarshal([]byte(s), &c)
}

// restage stages the changes a failed release carried again, each one only
// if it is still the live value (nothing changed it since), puts back an
// image setting its deploy changed, and notes the failure on the
// environment. A change staged since stays staged.
func (c *Core) restage(ctx context.Context, ss svcScope, rel store.Release, reason string) {
	ch, err := decodeCarried(rel.Carried)
	if err != nil {
		c.log.Error("read carried changes", "release", rel.ID, "err", err)
		return
	}
	if ch.empty() {
		return
	}
	note := fmt.Sprintf("release %d of %s failed (%s); the changes it carried are staged again", rel.Number, ss.svc.Name, reason)
	n := 0
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		if n, err = restageIn(ctx, q, ss.env.ID, ss.svc.ID, ch); err != nil || n == 0 {
			return err
		}
		return q.SetEnvironmentApplyError(ctx, store.SetEnvironmentApplyErrorParams{ApplyError: note, ID: ss.env.ID})
	})
	if err != nil {
		c.log.Error("restage changes of failed release", "release", rel.ID, "err", err)
		return
	}
	if n > 0 {
		what := "The change it carried is"
		if n > 1 {
			what = fmt.Sprintf("The %d changes it carried are", n)
		}
		c.releaseEvent(ctx, ss, rel, what+" staged again; see `dock changes`")
		c.audit(ctx, System, "changes.restage", &ss.project, &ss.env, ss.String(), note)
	}
}

// restageIn stages ch, applied to a service of an environment, again in
// q's transaction (see restage), and returns how many changes it staged.
func restageIn(ctx context.Context, q *store.Queries, envID, serviceID int64, ch carried) (int, error) {
	n := 0
	for _, v := range ch.Variables {
		ok, err := restageVariable(ctx, q, envID, v)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	if s := ch.Settings; s != nil {
		svc, err := q.GetService(ctx, serviceID)
		if err != nil {
			return n, err
		}
		if settingsOf(svc) == s.New {
			if err := setSettings(ctx, q, svc.ID, s.Old); err != nil {
				return n, err
			}
			staged := s.New
			if s.Staged != nil {
				staged = *s.Staged
			}
			if staged != s.Old {
				if svc.PendingSettings == nil {
					b, _ := json.Marshal(staged)
					next := string(b)
					if err := q.SetPendingSettings(ctx, store.SetPendingSettingsParams{PendingSettings: &next, ID: svc.ID}); err != nil {
						return n, err
					}
				}
				n++
			}
		}
	}
	for _, id := range ch.Volumes {
		if err := q.UnapplyVolume(ctx, id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// restageVariable puts one applied variable change back to staged, if the
// applied value is still live. It reports whether it did.
func restageVariable(ctx context.Context, q *store.Queries, envID int64, v carriedVar) (bool, error) {
	cur, err := q.GetVariable(ctx, store.GetVariableParams{EnvironmentID: envID, ServiceID: v.ServiceID, Key: v.Key})
	if v.New == nil { // an unset: the row was deleted
		if !store.IsNotFound(err) {
			return false, err // set again since (or a real error)
		}
		return true, q.InsertRestoredVariable(ctx, store.InsertRestoredVariableParams{
			EnvironmentID: envID, ServiceID: v.ServiceID, Key: v.Key, Value: v.Old, UpdatedAt: now(),
		})
	}
	if store.IsNotFound(err) || err == nil && !bytes.Equal(cur.Value, v.New) {
		return false, nil // removed or changed since
	}
	if err != nil {
		return false, err
	}
	pending, pendingValue := cur.Pending, cur.PendingValue
	switch {
	case pending == "":
		pending, pendingValue = "set", v.New
	case pending == "unset" && v.Old == nil:
		// Added by the failed release and staged for removal since: it
		// never was live, so there's nothing to keep.
		return true, q.DeleteVariable(ctx, cur.ID)
	}
	return true, q.RestoreVariable(ctx, store.RestoreVariableParams{
		Value: v.Old, Pending: pending, PendingValue: pendingValue, UpdatedAt: now(), ID: cur.ID,
	})
}

// setSettings replaces a service's live settings.
func setSettings(ctx context.Context, q *store.Queries, id int64, s apitypes.ServiceSettings) error {
	_, err := q.UpdateService(ctx, store.UpdateServiceParams{
		Image: s.Image, Port: int64(s.Port), HealthCheckPath: s.HealthCheckPath,
		HealthCheckTimeoutSeconds: int64(s.HealthCheckTimeoutSeconds), StartCommand: s.StartCommand,
		DockerfilePath: s.DockerfilePath, BuildCommand: s.BuildCommand,
		MemoryMb: int64(s.MemoryMB), Cpus: s.CPUs,
		HealthCheckIntervalSeconds:     int64(s.HealthCheckIntervalSeconds),
		HealthCheckProbeTimeoutSeconds: int64(s.HealthCheckProbeTimeoutSeconds),
		HealthCheckRetries:             int64(s.HealthCheckRetries),
		HealthCheckStartPeriodSeconds:  int64(s.HealthCheckStartPeriodSeconds),
		ID:                             id,
	})
	return err
}

// carriedBy is what a release of one service carries from an apply: the
// shared variable changes plus the service's own changes.
func (a applied) carriedBy(serviceID int64) carried {
	out := carried{Variables: slices.Clone(a.shared)}
	if own := a.own[serviceID]; own != nil {
		out.Variables = append(out.Variables, own.Variables...)
		out.Settings, out.Volumes = own.Settings, own.Volumes
	}
	return out
}
