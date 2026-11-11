package mcp

import (
	"reflect"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// discard_changes names changes as short strings; the first dot splits
// the service from the rest (hostnames keep theirs).
func TestDiscardInput(t *testing.T) {
	got := discardInput{
		Variables: []string{"SHARED", "web.KEY"}, Settings: []string{"web.port"},
		Volumes: []string{"db.data"}, Domains: []string{"web", "web.shop.example.com"}, Services: []string{"worker"},
	}.request()
	want := apitypes.DiscardChangesRequest{
		Variables: []apitypes.VariableRef{{Key: "SHARED"}, {Service: "web", Key: "KEY"}},
		Settings:  []apitypes.SettingRef{{Service: "web", Field: "port"}},
		Volumes:   []apitypes.VolumeRef{{Service: "db", Name: "data"}},
		Domains:   []apitypes.DomainRef{{Service: "web"}, {Service: "web", Hostname: "shop.example.com"}},
		Services:  []string{"worker"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request = %+v\nwant %+v", got, want)
	}
}
