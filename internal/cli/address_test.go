package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Setting the address prints the DNS record until it's live; removing it
// leaves the private address.
func TestAddressCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	srv.rt.SetDomainCheck(func(string) node.DomainCheck { return node.DomainCheck{Error: "no DNS record yet"} })

	code, stdout, stderr := exec(t, "address", "set", "dock.example.com")
	if code != 0 || !strings.Contains(stdout, "Point DNS:  A  dock.example.com  →  ") || !strings.Contains(stdout, "waiting for dns") {
		t.Fatalf("address set = %d\n%s\n%s", code, stdout, stderr)
	}
	if a := ok[apitypes.Address](t, "address"); a.URL != "https://dock.example.com" {
		t.Fatalf("address = %+v", a)
	}
	if a := ok[apitypes.Address](t, "address", "remove"); a.Hostname != "" || a.PrivateURL == "" {
		t.Fatalf("removed = %+v", a)
	}
}
