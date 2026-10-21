package cli

import (
	"errors"
	"os"

	"github.com/NordTempo/platform/internal/config"
	"github.com/NordTempo/platform/internal/node"
	"github.com/NordTempo/platform/internal/node/fake"
)

// newRuntime builds the local server's node runtime. PLATFORM_FAKE_RUNTIME=1
// uses an in-memory fake (no Docker), for local development of the API, CLI
// and UI.
//
// TODO(integration): return node.Local{Containers: docker, Builder: build,
// Proxy: caddy} built from cfg.
func newRuntime(_ config.Config) (node.Runtime, error) {
	if os.Getenv("PLATFORM_FAKE_RUNTIME") == "1" {
		return fake.New(), nil
	}
	return nil, errors.New("local runtime not wired yet; set PLATFORM_FAKE_RUNTIME=1 to run with a fake runtime")
}
