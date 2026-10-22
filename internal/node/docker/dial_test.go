package docker

import (
	"net/netip"
	"testing"

	"github.com/NordTempo/tempoDock/internal/node"
)

func TestEnvEndpoint(t *testing.T) {
	ct := node.Labels{Project: "1", Environment: "2", Service: "3", Release: "4"}
	ep := func(addr string, labels map[string]string) endpoint {
		var a netip.Addr
		if addr != "" {
			a = netip.MustParseAddr(addr)
		}
		return endpoint{labels: labels, addr: a}
	}
	env := node.Labels{Project: "1", Environment: "2"}.Map()
	legacy := node.Labels{}.Map() // managed, from before networks were labelled
	other := node.Labels{Project: "1", Environment: "9"}.Map()
	unmanaged := map[string]string{}

	for _, tc := range []struct {
		name string
		eps  []endpoint
		want string // "" means none
	}{
		{"none", nil, ""},
		{"environment network", []endpoint{ep("10.213.0.2", env)}, "10.213.0.2"},
		{"legacy network", []endpoint{ep("172.18.0.2", legacy)}, "172.18.0.2"},
		{"exact label beats legacy", []endpoint{ep("172.18.0.2", legacy), ep("10.213.0.2", env)}, "10.213.0.2"},
		{"never unmanaged", []endpoint{ep("172.17.0.2", unmanaged)}, ""},
		{"never another environment", []endpoint{ep("10.213.5.2", other)}, ""},
		{"skips unmanaged and foreign", []endpoint{ep("172.17.0.2", unmanaged), ep("10.213.5.2", other), ep("10.213.0.2", env)}, "10.213.0.2"},
		{"no address", []endpoint{ep("", env)}, ""},
		{"first of equals", []endpoint{ep("10.213.0.2", env), ep("10.213.1.2", env)}, "10.213.0.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := envEndpoint(ct, tc.eps)
			if tc.want == "" {
				if ok {
					t.Fatalf("got %s, want none", got)
				}
				return
			}
			if !ok || got.String() != tc.want {
				t.Fatalf("got %s %v, want %s", got, ok, tc.want)
			}
		})
	}
}
