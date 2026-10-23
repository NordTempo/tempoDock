package apitypes

import "time"

// UpdateStatus is what the console and `dock update` show about updating
// Dock (ADR 0028): the version this control plane runs, the releases
// newer than it, each server's version, and the update running or last
// finished. Operators only.
type UpdateStatus struct {
	// Version is the control plane's version.
	Version string `json:"version" example:"v0.3.0"`
	// Source is where releases come from: github.com/OWNER/REPO or a
	// directory.
	Source string `json:"source" example:"github.com/NordTempo/tempoDock"`
	// Latest is the newest release; empty when the check failed or there
	// are none.
	Latest string `json:"latest,omitempty" example:"v0.4.0"`
	// Available: Latest is newer than Version.
	Available bool `json:"available"`
	// Releases are the releases newer than Version, newest first, with
	// their notes. Pre-releases are left out unless Version is one.
	Releases  []UpdateRelease `json:"releases"`
	CheckedAt *time.Time      `json:"checked_at,omitempty"`
	// CheckError is why the check failed, saying what to do.
	CheckError string `json:"check_error,omitempty"`
	// Updater: dock-update.path is installed, so the control plane can
	// update itself. Without it the first update is by hand (install.sh).
	Updater bool `json:"updater"`
	// Running is the update in progress; nil when none is.
	Running *UpdateRun `json:"running,omitempty"`
	// Last is how the control plane's last update ended; nil if it never
	// updated itself.
	Last    *UpdateResult  `json:"last,omitempty"`
	Servers []UpdateServer `json:"servers"`
}

// UpdateRelease is a published release.
type UpdateRelease struct {
	Version     string    `json:"version" example:"v0.4.0"`
	Name        string    `json:"name,omitempty"`
	Notes       string    `json:"notes" doc:"Release notes, Markdown"`
	URL         string    `json:"url,omitempty"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
}

// UpdateStage is where a running update is.
type UpdateStage string

const (
	// UpdateDownloading: the control plane downloads and verifies the
	// release.
	UpdateDownloading UpdateStage = "downloading"
	// UpdateApplying: the updater is applying it to the control plane; the
	// API restarts.
	UpdateApplying UpdateStage = "applying"
	// UpdateServers: the control plane runs the new version and updates
	// the nodes behind it, one at a time.
	UpdateServers UpdateStage = "servers"
)

// UpdateRun is an update in progress.
type UpdateRun struct {
	Version     string      `json:"version"`
	Stage       UpdateStage `json:"stage" enum:"downloading,applying,servers"`
	RequestedBy string      `json:"requested_by,omitempty"`
	StartedAt   time.Time   `json:"started_at"`
}

// UpdateResult is how the control plane's update ended.
type UpdateResult struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status" enum:"succeeded,rolled_back,failed"`
	// Error is why it failed, saying what to do.
	Error       string    `json:"error,omitempty"`
	RequestedBy string    `json:"requested_by,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	// Backup is the control plane backup taken before the update, on the
	// control plane (root only).
	Backup string `json:"backup,omitempty"`
	// Log is the end of the updater's output.
	Log string `json:"log,omitempty"`
}

// UpdateServer is one server's version and update state.
type UpdateServer struct {
	Name    string       `json:"name"`
	Local   bool         `json:"local" doc:"Runs the control plane"`
	Status  ServerStatus `json:"status" enum:"online,offline,incompatible,joining,revoked"`
	Version string       `json:"version"`
	// Behind: it runs another version than the control plane.
	Behind bool `json:"behind"`
	// Manual is set when it can't be updated over its link (a node before
	// protocol 4): what to run on it instead.
	Manual string `json:"manual,omitempty"`
	// State is its update in this run, or its last one since the daemon
	// started: updating, updated, failed or skipped.
	State string `json:"state,omitempty" enum:"updating,updated,failed,skipped"`
	Error string `json:"error,omitempty"`
}

// StartUpdateRequest starts an update.
type StartUpdateRequest struct {
	// Version to update to; empty is the newest release.
	Version string `json:"version,omitempty" example:"v0.4.0"`
}
