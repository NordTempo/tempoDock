package backup

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A Postgres snapshot's stream restores by its first bytes: one from
// before roles were saved is pg_dump's output alone (it starts with
// PGDMP) and reaches pg_restore whole, byte for byte, with no roles run;
// one with the DOCKPG1 header has its roles run through psql and the rest
// handed to pg_restore.
//
// The real unpack step runs under /bin/sh with stub binaries: `psql`
// keeps the roles file it was given, `pg_restore` what it read.
func TestPostgresRestoreReadsBothStreamFormats(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	// Bytes a text tool would mangle, longer than the header.
	dump := append([]byte("PGDMP\x01\x0e\x00\x04\x08\x00\n\r\xff"), bytes.Repeat([]byte("row\x00\n"), 64)...)
	roles := "CREATE ROLE tenant;\nALTER ROLE tenant WITH NOLOGIN;\n"
	for _, tc := range []struct {
		name, roles string
		stream      []byte
	}{
		{name: "before roles were saved", stream: dump},
		{name: "shorter than the header", stream: dump[:9]},
		{name: "with roles", roles: roles, stream: append(fmt.Appendf(nil, "%s %012d\n%s", PostgresMagic, len(roles), roles), dump...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			restored, ran := filepath.Join(dir, "restored"), filepath.Join(dir, "roles.ran")
			stub := func(name, body string) {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			stub("pg_restore", "cat >"+restored+"\n")
			stub("psql", `while [ "$#" -gt 1 ]; do [ "$1" = -f ] && cp "$2" `+ran+`; shift; done`+"\n")
			// The job's scratch files, in the test's directory.
			script := strings.NewReplacer(pgHeadFile, filepath.Join(dir, "head"), pgRolesFile, filepath.Join(dir, "roles.sql")).Replace(pgUnpack)
			cmd := exec.Command("sh", "-c", "set -eu\n"+script)
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "PGDATABASE=app"}
			cmd.Stdin = bytes.NewReader(tc.stream)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("unpack: %v\n%s", err, out)
			}
			want := dump
			if tc.roles == "" {
				want = tc.stream
			}
			if got, err := os.ReadFile(restored); err != nil || !bytes.Equal(got, want) {
				t.Errorf("pg_restore read %d bytes (%v), want the dump's %d unchanged", len(got), err, len(want))
			}
			got, err := os.ReadFile(ran)
			if tc.roles == "" {
				if err == nil {
					t.Errorf("psql ran roles for a stream without the header: %q", got)
				}
			} else if string(got) != tc.roles {
				t.Errorf("psql ran %q (%v), want the roles", got, err)
			}
		})
	}
}
