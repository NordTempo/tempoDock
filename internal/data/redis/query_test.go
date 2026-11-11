package redis

import (
	"errors"
	"testing"

	"github.com/NordTempo/tempoDock/internal/data"
)

// Commands that walk a whole value in one call are refused past the
// console's limit; the server is never asked.
func TestCheckSizeWalks(t *testing.T) {
	for line, ok := range map[string]bool{
		"SCAN 0 COUNT 100":                   true,
		"SCAN 0 MATCH a* COUNT 1000000000":   false,
		"HSCAN h 0 COUNT 0":                  false,
		"XINFO STREAM s":                     true,
		"XINFO STREAM s FULL COUNT 10":       true,
		"XINFO STREAM s FULL":                false,
		"XINFO STREAM s FULL COUNT 0":        false,
		"MEMORY USAGE k":                     true,
		"MEMORY USAGE k SAMPLES 0":           false,
		"MEMORY USAGE k SAMPLES 99999999999": false,
	} {
		args, err := parseCommand(line)
		if err != nil {
			t.Fatal(err)
		}
		err = checkSize(nil, args, 1000)
		if (err == nil) != ok || (err != nil && !errors.Is(err, data.ErrInvalid)) {
			t.Errorf("%s: %v", line, err)
		}
	}
}
