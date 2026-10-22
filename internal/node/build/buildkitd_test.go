package build

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/moby/buildkit/exporter/containerimage/exptypes"

	"github.com/NordTempo/tempoDock/internal/node"
)

// A BuildKit container is used only by the runtime that created it: two
// runtimes on one Docker host would otherwise recreate each other's and
// kill builds. One from before owners were recorded is taken over.
func TestCheckOwner(t *testing.T) {
	b := &Builder{name: ContainerName, Owner: "/var/lib/dock"}
	ours := map[string]string{node.LabelManaged: "true", node.LabelRole: roleBuildKit}
	if err := b.checkOwner(ours); err != nil {
		t.Errorf("container without an owner: %v", err)
	}
	ours[node.LabelOwner] = "/var/lib/dock"
	if err := b.checkOwner(ours); err != nil {
		t.Errorf("own container: %v", err)
	}
	theirs := map[string]string{node.LabelManaged: "true", node.LabelRole: roleBuildKit, node.LabelOwner: "/tmp/other"}
	if err := b.checkOwner(theirs); err == nil || !strings.Contains(err.Error(), "one dock runtime per Docker host") {
		t.Errorf("another runtime's container: %v", err)
	}
	if err := b.checkOwner(map[string]string{"com.example": "x"}); err == nil || !strings.Contains(err.Error(), "didn't create") {
		t.Errorf("a stranger's container: %v", err)
	}
}

// labelImage marks every image config in a frontend's result, keeping the
// image's own labels and other settings, and nothing else.
func TestLabelImage(t *testing.T) {
	meta := map[string][]byte{
		exptypes.ExporterImageConfigKey:                  []byte(`{"architecture":"amd64","config":{"Env":["A=1"],"Labels":{"app":"web"},"Healthcheck":{"Test":["NONE"]}}}`),
		exptypes.ExporterImageConfigKey + "/linux/arm64": []byte(`{"architecture":"arm64"}`),
		exptypes.ExporterImageBaseConfigKey:              []byte(`{"config":{}}`),
	}
	if err := labelImage(meta); err != nil {
		t.Fatal(err)
	}
	var img struct {
		Architecture string
		Config       struct {
			Env         []string
			Labels      map[string]string
			Healthcheck map[string]any
		}
	}
	if err := json.Unmarshal(meta[exptypes.ExporterImageConfigKey], &img); err != nil {
		t.Fatal(err)
	}
	if img.Architecture != "amd64" || len(img.Config.Env) != 1 || img.Config.Healthcheck == nil ||
		img.Config.Labels["app"] != "web" || img.Config.Labels[node.LabelImage] != "true" {
		t.Errorf("labelled config = %s", meta[exptypes.ExporterImageConfigKey])
	}
	if !strings.Contains(string(meta[exptypes.ExporterImageConfigKey+"/linux/arm64"]), `"`+node.LabelImage+`":"true"`) {
		t.Errorf("platform config = %s", meta[exptypes.ExporterImageConfigKey+"/linux/arm64"])
	}
	if string(meta[exptypes.ExporterImageBaseConfigKey]) != `{"config":{}}` {
		t.Errorf("base config changed: %s", meta[exptypes.ExporterImageBaseConfigKey])
	}
}

func TestCgroupProblem(t *testing.T) {
	const mem = 512 << 20
	cases := []struct {
		out  string
		want string // substring; "" means no problem
	}{
		{"536870912\n0\n", ""},
		{"536870912\nmax\n", "memory.swap.max is max"},
		{"max\n0\n", "memory.max is max"},
		{"unknown\nunknown\n", ""},   // cgroup v1: can't tell
		{"536870912\nunknown\n", ""}, // swap accounting off
		{"", ""},                     // exec failed
	}
	for _, tc := range cases {
		got := cgroupProblem(tc.out, mem)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("cgroupProblem(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}
