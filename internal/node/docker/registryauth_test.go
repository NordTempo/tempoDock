package docker

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/registry"
)

// PasswordAuth names the registry the image comes from, Docker Hub as
// Docker's own login does, and carries the password as is, never as a
// bearer token.
func TestPasswordAuth(t *testing.T) {
	for _, tc := range []struct{ ref, server string }{
		{"ghcr.io/acme/app:1", "ghcr.io"},
		{"registry.example.com:5000/app@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "registry.example.com:5000"},
		{"acme/app", dockerHubAddress},
		{"nginx", dockerHubAddress},
		{"index.docker.io/library/nginx", dockerHubAddress},
	} {
		raw, err := base64.URLEncoding.DecodeString(PasswordAuth(tc.ref, "bot", "s3cret"))
		if err != nil {
			t.Fatal(err)
		}
		var got registry.AuthConfig
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		want := registry.AuthConfig{Username: "bot", Password: "s3cret", ServerAddress: tc.server}
		if got != want {
			t.Errorf("%s: auth = %+v, want %+v", tc.ref, got, want)
		}
	}
}
