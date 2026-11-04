package docker

import (
	"encoding/base64"
	"encoding/json"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/registry"
)

// RegistryAuth encodes a registry bearer token as Docker's X-Registry-Auth
// value. Docker sends RegistryToken to the registry as is. The token is
// also the password, for pull paths that only know the token flow with
// credentials: they present it to the registry's token endpoint, which
// hands it back (registry.Signer.Handler).
func RegistryAuth(token string) string {
	return encodeAuth(registry.AuthConfig{Username: "dock", Password: token, RegistryToken: token})
}

// dockerHubAddress is the server address Docker's own login records for
// Docker Hub.
const dockerHubAddress = "https://index.docker.io/v1/"

// PasswordAuth encodes a username and password (or access token) for the
// registry ref is pulled from as Docker's X-Registry-Auth value (ADR
// 0051). Docker signs in to that registry with them, by basic auth or its
// token endpoint, whichever the registry asks for.
func PasswordAuth(ref, username, password string) string {
	auth := registry.AuthConfig{Username: username, Password: password}
	if named, err := reference.ParseNormalizedNamed(ref); err == nil {
		auth.ServerAddress = reference.Domain(named)
		if auth.ServerAddress == "docker.io" {
			auth.ServerAddress = dockerHubAddress
		}
	}
	return encodeAuth(auth)
}

// encodeAuth is X-Registry-Auth's encoding: URL-safe base64 of the JSON.
func encodeAuth(auth registry.AuthConfig) string {
	b, _ := json.Marshal(auth)
	return base64.URLEncoding.EncodeToString(b)
}
