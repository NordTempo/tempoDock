package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// An empty value where empty would mean something else (a variable that
// was never set) is refused before anything connects: `deploy --image ""`
// would otherwise pack and upload the current directory.
func TestBlankFlagsRefused(t *testing.T) {
	setup(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"deploy", "--image", ""}, "--image is empty"},
		{[]string{"deploy", "--image", " ", "-s", "web"}, "--image is empty"},
		{[]string{"deploy", ""}, "the directory to deploy is empty"},
		{[]string{"template", "save", "--file", ""}, "--file is empty"},
	} {
		code, _, stderr := exec(t, tc.args...)
		if code != int(output.ExitUsage) || !strings.Contains(stderr, tc.want) {
			t.Errorf("%q: exit %d, %s; want usage error %q", tc.args, code, stderr, tc.want)
		}
	}
}
