package docker

import (
	"encoding/base64"
	"encoding/json"

	"github.com/moby/moby/api/types/registry"
)

// RegistryAuth encodes a registry bearer token as Docker's X-Registry-Auth
// value. Docker sends RegistryToken to the registry as is. The token is
// also the password, for pull paths that only know the token flow with
// credentials: they present it to the registry's token endpoint, which
// hands it back (registry.Signer.Handler).
func RegistryAuth(token string) string {
	b, _ := json.Marshal(registry.AuthConfig{Username: "dock", Password: token, RegistryToken: token})
	return base64.URLEncoding.EncodeToString(b)
}
