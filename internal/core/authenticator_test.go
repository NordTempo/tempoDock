package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// authenticator is a software passkey for tests: an ES256 key that
// answers WebAuthn ceremonies like a browser and authenticator would,
// with "none" attestation. Its fields can be bent to test refusals.
type authenticator struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	id     []byte
	handle []byte
	// origin is what the browser reports in clientDataJSON; rpID is what
	// the authenticator hashes into authenticatorData.
	origin string
	rpID   string
	count  uint32
	// noUV leaves out user verification (no PIN or biometrics).
	noUV bool
}

var b64 = base64.RawURLEncoding

func newAuthenticator(t *testing.T, origin, rpID string) *authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &authenticator{t: t, key: key, id: id, origin: origin, rpID: rpID}
}

// options decodes a challenge's options.
func (a *authenticator) options(ch apitypes.PasskeyChallenge) (challenge string, userID []byte) {
	var o struct {
		Challenge string `json:"challenge"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(ch.Options, &o); err != nil {
		a.t.Fatal(err)
	}
	if o.User.ID != "" {
		id, err := b64.DecodeString(o.User.ID)
		if err != nil {
			a.t.Fatal(err)
		}
		userID = id
	}
	return o.Challenge, userID
}

func (a *authenticator) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.origin, "crossOrigin": false})
	return b
}

// authData is authenticatorData: user present and verified (unless
// noUV), plus the attested credential when attested.
func (a *authenticator) authData(attested bool) []byte {
	rp := sha256.Sum256([]byte(a.rpID))
	flags := byte(0x01) // UP
	if !a.noUV {
		flags |= 0x04 // UV
	}
	if attested {
		flags |= 0x40 // AT
	}
	out := append(rp[:], flags)
	out = binary.BigEndian.AppendUint32(out, a.count)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.id)))
		out = append(out, a.id...)
		x, y := make([]byte, 32), make([]byte, 32)
		a.key.X.FillBytes(x)
		a.key.Y.FillBytes(y)
		cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		if err != nil {
			a.t.Fatal(err)
		}
		out = append(out, cose...)
	}
	return out
}

// register answers a registration challenge.
func (a *authenticator) register(ch apitypes.PasskeyChallenge) apitypes.PasskeyResponse {
	challenge, handle := a.options(ch)
	a.handle = handle
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(true)})
	if err != nil {
		a.t.Fatal(err)
	}
	cred, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", challenge)),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	})
	return apitypes.PasskeyResponse{Ceremony: ch.Ceremony, Credential: cred}
}

// assert answers a sign-in challenge, counting a use.
func (a *authenticator) assert(ch apitypes.PasskeyChallenge) apitypes.PasskeyResponse {
	challenge, _ := a.options(ch)
	a.count++
	data := a.authData(false)
	cd := a.clientData("webauthn.get", challenge)
	sum := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, data...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}
	cred, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(data),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.handle),
		},
		"clientExtensionResults": map[string]any{},
	})
	return apitypes.PasskeyResponse{Ceremony: ch.Ceremony, Credential: cred}
}
