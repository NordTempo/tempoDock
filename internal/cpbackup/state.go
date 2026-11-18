package cpbackup

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrLocked is a data dir a running control plane holds.
var ErrLocked = errors.New("the control plane is running on this data dir; stop it first: sudo systemctl stop dock")

// Lock takes the data dir's exclusive lock and returns its release. `dock
// serve` holds it while it runs; `dock admin backup --move` and `dock
// admin restore` take it, so neither runs under a live daemon, and two
// daemons never share a data dir. It is an flock on the directory itself:
// no lock file to own or clean up, and it ends with the process.
func Lock(dataDir string) (func(), error) {
	f, err := os.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", dataDir, err)
	}
	return func() { _ = f.Close() }, nil
}

// Moved is what MovedFile records.
type Moved struct {
	At time.Time `json:"at"`
	// Archive is where `dock admin backup --move` wrote the backup.
	Archive string `json:"archive"`
}

// MarkMoved retires the control plane in dataDir: `dock serve` refuses to
// start there from now on (CheckMoved).
func MarkMoved(dataDir string, m Moved) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(filepath.Join(dataDir, MovedFile), append(b, '\n'), 0o600)
}

// ErrMoved is a control plane that moved away; CheckMoved wraps it.
var ErrMoved = errors.New("this control plane moved to another server")

// CheckMoved fails with ErrMoved, saying what to do, if the control plane
// in dataDir was retired by `dock admin backup --move`.
func CheckMoved(dataDir string) error {
	p := filepath.Join(dataDir, MovedFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var m Moved
	_ = json.Unmarshal(b, &m)
	when := "earlier"
	if !m.At.IsZero() {
		when = "on " + m.At.Local().Format("2006-01-02 15:04")
	}
	return fmt.Errorf("%w %s (`dock admin backup --move`): starting it here would fight the new one for the nodes and apps. If the move was abandoned and nothing was restored elsewhere, delete %s and start dock again", ErrMoved, when, p)
}

// RecoveryFile, in the data dir, puts the control plane in recovery mode
// (ADR 0098): it was restored onto a server whose apps and volumes are
// not back yet.
const RecoveryFile = "recovery.json"

// Recovery is what RecoveryFile records.
type Recovery struct {
	At time.Time `json:"at"`
	// From describes the archive the control plane was restored from.
	From string `json:"from"`
	// Token is what `dock admin recover` proves it can read the data dir
	// with, to the daemon's loopback listener.
	Token string `json:"token"`
}

// MarkRecovering puts the control plane in dataDir in recovery mode,
// with a new token. `dock admin restore --recovery` calls it before the
// daemon's first start.
func MarkRecovering(dataDir, from string) error {
	b, _ := json.Marshal(Recovery{At: time.Now().UTC(), From: from, Token: rand.Text()})
	return os.WriteFile(filepath.Join(dataDir, RecoveryFile), append(b, '\n'), 0o600)
}

// ReadRecovery returns dataDir's recovery mode, or nil when it is not in
// it.
func ReadRecovery(dataDir string) (*Recovery, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, RecoveryFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Recovery
	if err := json.Unmarshal(b, &r); err != nil || r.Token == "" {
		return nil, fmt.Errorf("%s is not a recovery file dock wrote; delete it to leave recovery mode", filepath.Join(dataDir, RecoveryFile))
	}
	return &r, nil
}

// EndRecovery takes dataDir out of recovery mode.
func EndRecovery(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, RecoveryFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
