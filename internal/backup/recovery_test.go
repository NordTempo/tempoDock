package backup

import (
	"testing"
	"time"
)

// The listing keeps each service's newest snapshot across its scheduled
// and manual tags, and leaves out base backups and the control plane's.
func TestParseNewest(t *testing.T) {
	out := `restic noise
` + NewestMarker + `
"tags":["dock-s3"]
"time":"2026-11-17T03:00:01.5+01:00"
"tags":["dock-s3"]
"id":"aaaa1111"
"tags":["dock-s3-manual"]
"time":"2026-11-18T10:00:00Z"
"tags":["dock-s3-manual"]
"id":"bbbb2222"
"tags":["dock-s3-base"]
"time":"2026-11-18T12:00:00Z"
"tags":["dock-s3-base"]
"id":"cccc3333"
"tags":["dock-control-plane"]
"time":"2026-11-18T12:00:00Z"
"tags":["dock-control-plane"]
"id":"dddd4444"
"tags":["dock-s12"]
"time":"2026-11-16T03:00:00Z"
"tags":["dock-s12"]
"id":"eeee5555"
`
	got, err := ParseNewest(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[3].Snapshot != "bbbb2222" || !got[3].At.Equal(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)) || got[12].Snapshot != "eeee5555" {
		t.Fatalf("newest = %+v", got)
	}
	if _, err := ParseNewest("restic: wrong password"); err == nil {
		t.Fatal("output without the marker was taken as an empty repository")
	}
}
