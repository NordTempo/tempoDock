package main

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"

	"github.com/NordTempo/tempoDock/internal/update"
)

// sshSigAsset holds the same signatures as update.SigAsset in OpenSSH's
// format, a line per key: "<key id> <SSHSIG blob, base64>". It is for the
// install scripts on machines whose openssl can't check Ed25519 (a stock
// Mac's LibreSSL) but whose ssh-keygen can (`ssh-keygen -Y verify`). dock
// itself never reads it.
const sshSigAsset = "SHA256SUMS.sshsig"

// sshSigNamespace is the namespace the signatures are made for, which
// `ssh-keygen -Y verify -n` must name: a signature made for anything else
// doesn't pass as a release's.
const sshSigNamespace = "dock-release"

// sshString appends s to b as an SSH wire string: its length, then its bytes.
func sshString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// signSSH returns priv's sshSigAsset line for sums, newline included: the
// signature OpenSSH's PROTOCOL.sshsig describes, over SHA-512 of sums.
func signSSH(priv ed25519.PrivateKey, sums []byte) string {
	const keyType = "ssh-ed25519"
	pub, _ := priv.Public().(ed25519.PublicKey)
	hash := sha512.Sum512(sums)

	signed := []byte("SSHSIG")
	signed = sshString(signed, []byte(sshSigNamespace))
	signed = sshString(signed, nil)
	signed = sshString(signed, []byte("sha512"))
	signed = sshString(signed, hash[:])

	key := sshString(sshString(nil, []byte(keyType)), pub)
	sig := sshString(sshString(nil, []byte(keyType)), ed25519.Sign(priv, signed))

	blob := []byte("SSHSIG")
	blob = binary.BigEndian.AppendUint32(blob, 1)
	blob = sshString(blob, key)
	blob = sshString(blob, []byte(sshSigNamespace))
	blob = sshString(blob, nil)
	blob = sshString(blob, []byte("sha512"))
	blob = sshString(blob, sig)
	return update.KeyID(pub) + " " + base64.StdEncoding.EncodeToString(blob) + "\n"
}
