package build

import (
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/jsonstream"

	"github.com/NordTempo/tempoDock/internal/node"
)

func TestPushedDigest(t *testing.T) {
	const d = "sha256:7f2fe6e84851dbdaae5da6f521fe47b6feefa9ce24a8927e086ff1a0a89c781b"
	aux := json.RawMessage(`{"Tag":"staging-1","Digest":"` + d + `","Size":668}`)
	other := json.RawMessage(`{"manifestPushedInsteadOfIndex":true}`)
	for _, tc := range []struct {
		msg  jsonstream.Message
		want string
	}{
		{jsonstream.Message{Status: "staging-1: digest: " + d + " size: 668"}, d}, // containerd image store
		{jsonstream.Message{Aux: &aux}, d},                                        // classic image store
		{jsonstream.Message{Aux: &other}, ""},
		{jsonstream.Message{Status: "Pushed", ID: "37bb94b0940b"}, ""},
	} {
		if got, _ := pushedDigest(tc.msg); got != tc.want {
			t.Errorf("pushedDigest(%+v) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

func TestCheckPushTarget(t *testing.T) {
	for ref, ok := range map[string]bool{
		"127.0.0.1:5000/p1/web:staging-1": true,
		"127.0.0.1:5000/p1/web@sha256:7f2fe6e84851dbdaae5da6f521fe47b6feefa9ce24a8927e086ff1a0a89c781b": false,
		"127.0.0.1:5000/P1/web:x": false,
	} {
		if err := checkPushTarget(&node.PushTarget{Ref: ref, Token: "t"}); (err == nil) != ok {
			t.Errorf("checkPushTarget(%s) = %v", ref, err)
		}
	}
	if checkPushTarget(&node.PushTarget{Ref: "127.0.0.1:5000/p1/web:1"}) == nil {
		t.Error("push target without a token accepted")
	}
}
