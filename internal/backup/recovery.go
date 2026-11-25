package backup

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NewestMarker precedes the listing in a NewestScript job's output.
const NewestMarker = "dock-newest-snapshots:"

// NewestScript lists the newest snapshot of every set of tags in the
// job's repository after NewestMarker, as "time", "tags" and "id" tokens
// (ParseNewest): what a recovery restores (ADR 0098). It reads without a
// lock and leaves the old server's locks alone (shell, not prelude):
// that server may be gone, and nothing is written. A
// repository that can't be opened fails the job with restic's own words.
// Kept this small, a repository of well over a thousand services fits in
// a job's output (node.MaxJobOutput), which the full JSON doesn't. It
// runs in the restic image.
const NewestScript = shell + `reach "$RESTIC_REPOSITORY" "$RESTIC_PASSWORD"
[ "$rc" = 0 ] || $R cat config --no-lock >/dev/null
echo ` + NewestMarker + `
$R snapshots --json --no-lock --latest 1 --group-by tags | grep -oE '"(time|id)":"[^"]*"|"tags":\[[^]]*\]' || true
`

// Newest is a service's newest snapshot in a repository.
type Newest struct {
	Snapshot string
	At       time.Time
}

// serviceTag matches the tags of a service's backups: the scheduled ones
// (Tag) and the manual ones (ADR 0095). A base backup for point-in-time
// restore (dock-s<id>-base) is not a backup a recovery loads.
var serviceTag = regexp.MustCompile(`"dock-s([0-9]+)(-manual)?"`)

// ParseNewest reads a NewestScript job's output: each service's newest
// snapshot, by service ID. restic prints a snapshot's time, then its
// tags, then its ID; a group's own tags come before its snapshots.
func ParseNewest(output string) (map[int64]Newest, error) {
	_, list, ok := strings.Cut(output, NewestMarker)
	if !ok {
		return nil, errors.New("the job listed no snapshots")
	}
	out := map[int64]Newest{}
	var at time.Time
	var service int64
	for line := range strings.Lines(list) {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch key {
		case `"time"`:
			at, _ = time.Parse(time.RFC3339Nano, strings.Trim(val, `"`))
		case `"tags"`:
			service = 0
			if m := serviceTag.FindStringSubmatch(val); m != nil {
				service, _ = strconv.ParseInt(m[1], 10, 64)
			}
		case `"id"`:
			if cur, seen := out[service]; service != 0 && !at.IsZero() && (!seen || at.After(cur.At)) {
				out[service] = Newest{Snapshot: strings.Trim(val, `"`), At: at}
			}
			at = time.Time{}
		}
	}
	return out, nil
}
