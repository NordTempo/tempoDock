//go:build integration

package build

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// A build can't land a secret-dependent layer in another scope's cache
// by how its Dockerfile is written: the mount's type spelled so only
// BuildKit's expansion reads "secret", and the other scope's cache key
// set by ENV ahead of the step. The other scope's build runs the step
// itself, with its own (here unset) secret.
func TestSecretStepKeyHoldsAgainstTheDockerfile(t *testing.T) {
	b := newBuilder(t, dockerClient(t), 1024)
	const step = `echo "installed from ${PIP_INDEX_URL:-the-public-index}" > /installed-from`
	nonce := fmt.Sprint(time.Now().UnixNano())
	scope, otherScope := "p7_e9_"+nonce, "p66_e99_"+nonce
	prefix := "FROM node:22-alpine\nRUN echo \"nonce " + nonce + "\"\n"
	// What the scope's own build keys its step with: its secret is unset.
	key := secretsKey(scope, map[string]string{}, []string{"PIP_INDEX_URL"})

	if _, logs, err := build(t, b, node.BuildRequest{
		Source: sourceTar(t, map[string]string{"Dockerfile": prefix +
			"ENV " + cacheKeyArg + "=" + key + "\n" +
			"RUN --mount=type=s\\\\ecret,id=PIP_INDEX_URL,env=PIP_INDEX_URL " + step + "\n"}),
		BuildArgs:  map[string]string{"PIP_INDEX_URL": "https://other.example/simple"},
		CacheScope: otherScope,
		Tag:        "dock-test/secretkey:other",
	}); err != nil {
		t.Fatalf("the other scope's build: %v\n%s", err, logs)
	}
	_, logs, err := build(t, b, node.BuildRequest{
		Source: sourceTar(t, map[string]string{"Dockerfile": prefix +
			"RUN --mount=type=secret,id=PIP_INDEX_URL,env=PIP_INDEX_URL " + step + "\n" +
			"RUN echo \"image: $(cat /installed-from)\"\n"}),
		BuildArgs:  map[string]string{},
		CacheScope: scope,
		Tag:        "dock-test/secretkey:own",
	})
	if err != nil {
		t.Fatalf("the scope's own build: %v\n%s", err, logs)
	}
	if !strings.Contains(logs, "image: installed from the-public-index") {
		t.Fatalf("the scope's image should hold its own step's layer:\n%s", logs)
	}
}
