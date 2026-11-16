package projectfile

import (
	"strings"
	"testing"
)

// Jobs parse from TOML and JSON, and their structure is checked.
func TestParseJobs(t *testing.T) {
	const head = "version = 1\n[[environments]]\nname = \"production\"\n[[environments.services]]\nname = \"web\"\n"
	f, err := Parse([]byte(head + "[[environments.services.jobs]]\nname = \"cleanup\"\nschedule = \"0 3 * * *\"\ntimezone = \"Europe/Oslo\"\ncommand = \"bin/cleanup\"\ntimeout_seconds = 60\noverlap = \"allow\"\nenabled = false\n"))
	if err != nil {
		t.Fatal(err)
	}
	j := f.Environments[0].Services[0].Jobs[0]
	if j.Name != "cleanup" || j.Schedule != "0 3 * * *" || j.Timezone != "Europe/Oslo" || j.TimeoutSeconds != 60 || j.Overlap != "allow" || j.Enabled == nil || *j.Enabled {
		t.Fatalf("job = %+v", j)
	}
	if _, err := Parse([]byte(`{"version":1,"environments":[{"name":"production","services":[{"name":"web","jobs":[{"name":"a","schedule":"@daily","command":"x"}]}]}]}`)); err != nil {
		t.Fatal(err)
	}
	for jobs, want := range map[string]string{
		"[[environments.services.jobs]]\nschedule = \"@daily\"\ncommand = \"x\"\n": "has no name",
		"[[environments.services.jobs]]\nname = \"a\"\ncommand = \"x\"\n":          "needs a schedule and a command",
		"[[environments.services.jobs]]\nname = \"a\"\nschedule = \"@daily\"\ncommand = \"x\"\n[[environments.services.jobs]]\nname = \"a\"\nschedule = \"@daily\"\ncommand = \"y\"\n": "in the file twice",
		"[[environments.services.jobs]]\nname = \"a\"\nschedule = \"@daily\"\ncommand = \"x\"\ntimeout = \"1h\"\n":                                                                     "jobs.timeout",
	} {
		if _, err := Parse([]byte(head + jobs)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", jobs, err, want)
		}
	}
}
