package compose

import (
	"slices"
	"testing"
)

func TestInterpolate(t *testing.T) {
	env := map[string]string{"SET": "v", "EMPTY": ""}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	for _, tc := range []struct {
		in, want string
		missing  []string
	}{
		{"plain", "plain", nil},
		{"$SET-$SET", "v-v", nil},
		{"${SET}x", "vx", nil},
		{"$$SET and $${SET}", "$SET and ${SET}", nil},
		{"${UNSET}", "", []string{"UNSET"}},
		{"$UNSET/x", "/x", []string{"UNSET"}},
		{"${UNSET:-def}", "def", nil},
		{"${EMPTY:-def}|${EMPTY-def}", "def|", nil},
		{"${UNSET-${SET}}", "v", nil},
		{"${UNSET:-${ALSO}}", "", []string{"ALSO"}},
		{"${UNSET:?needed}", "", []string{"UNSET"}},
		{"${EMPTY?needed}", "", nil},
		{"${SET:+alt}${UNSET:+alt}", "alt", nil},
		{"${{db.URL}}", "${{db.URL}}", nil},
		{"cost: $5 and ${", "cost: $5 and ${", nil},
	} {
		got, missing := interpolate(tc.in, lookup)
		if got != tc.want || !slices.Equal(missing, tc.missing) {
			t.Errorf("interpolate(%q) = %q, %v; want %q, %v", tc.in, got, missing, tc.want, tc.missing)
		}
	}
}

func TestParseEnvFile(t *testing.T) {
	content := "# comment\n\nexport A=1\nB = two words # comment\nC='lit $A'\nD=\"x\\n$A\"\nE=\"multi\nline\"\nF\nnot a variable\nG=${A}${H:-h}\nI=#notcomment\n"
	lines, bad := parseEnvFile(content, nil)
	got := map[string]string{}
	for _, l := range lines {
		if l.bare {
			got[l.key] = "<bare>"
		} else {
			got[l.key] = l.value
		}
	}
	want := map[string]string{
		"A": "1", "B": "two words", "C": "lit $A", "D": "x\n1", "E": "multi\nline", "F": "<bare>", "G": "1h", "I": "#notcomment",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if !slices.Equal(bad, []int{10}) {
		t.Errorf("bad lines %v, want [10]", bad)
	}
}

func TestParsePort(t *testing.T) {
	for _, tc := range []struct {
		in        string
		target    int
		end       int
		published bool
		hostIP    string
		protocol  string
	}{
		{"80", 80, 0, false, "", "tcp"},
		{"8080:80", 80, 0, true, "", "tcp"},
		{"127.0.0.1:8080:80", 80, 0, true, "127.0.0.1", "tcp"},
		{"8080:80/udp", 80, 0, true, "", "udp"},
		{"8000-8010:8000-8010", 8000, 8010, true, "", "tcp"},
		{"[::1]:8080:80", 80, 0, true, "::1", "tcp"},
		{"127.0.0.1::80", 80, 0, true, "127.0.0.1", "tcp"},
	} {
		p, err := parsePort(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if p.target != tc.target || p.targetEnd != tc.end || p.published != tc.published || p.hostIP != tc.hostIP || p.protocol != tc.protocol {
			t.Errorf("%q: got %+v", tc.in, p)
		}
	}
	for _, bad := range []string{"", "http", "1:2:3:4", "70000", "90-80"} {
		if _, err := parsePort(bad); err == nil {
			t.Errorf("%q: parsed, want an error", bad)
		}
	}
}

func TestParseMemory(t *testing.T) {
	for in, want := range map[string]int{
		"512m": 512, "512M": 512, "1g": 1024, "1gb": 1024, "1.5G": 1536, "256Mi": 256, "536870912": 512, "100k": 1, "2048KB": 2,
	} {
		got, err := parseMemory(in)
		if err != nil || got != want {
			t.Errorf("parseMemory(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "lots", "1p", "-1g"} {
		if _, err := parseMemory(bad); err == nil {
			t.Errorf("parseMemory(%q) parsed, want an error", bad)
		}
	}
}

func TestHTTPCheck(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		path string
		port int
		ok   bool
	}{
		{"curl -f http://localhost:3000/health", "/health", 3000, true},
		{"curl -fsS -o /dev/null http://127.0.0.1/", "/", 80, true},
		{"wget -qO- --tries=1 http://0.0.0.0:8080/api/ready?full=1", "/api/ready?full=1", 8080, true},
		{"wget --spider localhost:9000", "/", 9000, true},
		{"/usr/bin/curl --fail http://localhost:$PORT/up", "/up", 0, true},
		{"curl -X POST http://localhost/health", "", 0, false},
		{"curl -f https://localhost/health", "", 0, false},
		{"curl -f http://example.com/health", "", 0, false},
		{"pg_isready -U app", "", 0, false},
	} {
		p, port, ok := httpCheck(shellWords(tc.cmd))
		if p != tc.path || port != tc.port || ok != tc.ok {
			t.Errorf("%q: got %q %d %v", tc.cmd, p, port, ok)
		}
	}
}

func TestShellJoin(t *testing.T) {
	if got, want := shellJoin([]string{"node", "dist/worker.js", "--queue", "default emails", "it's"}), `node dist/worker.js --queue 'default emails' 'it'\''s'`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
