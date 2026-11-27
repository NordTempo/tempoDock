package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// `dock certificate add` reads the chain from a file and the key from
// stdin and never prints the key; `list` and `remove` follow.
func TestCertificateCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Self"}, DNSNames: []string{"*.corp.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(100 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	chain := filepath.Join(t.TempDir(), "chain.pem")
	if err := os.WriteFile(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := exec(t, "certificate", "add", "--cert", chain); code != int(output.ExitUsage) || !strings.Contains(stderr, "--key are required") {
		t.Fatalf("no key: %d %s", code, stderr)
	}
	code, stdout, stderr := execStdin(t, keyPEM, "certificate", "add", "--cert", chain, "--key", "-", "--json")
	if code != 0 || !strings.Contains(stdout, `"*.corp.test"`) || strings.Contains(stdout+stderr, "PRIVATE KEY") {
		t.Fatalf("add: %d\n%s%s", code, stdout, stderr)
	}
	list := ok[[]apitypes.Certificate](t, "certificate", "list")
	if len(list) != 1 || list[0].Status != apitypes.CertificateUnused || list[0].Public {
		t.Fatalf("list = %+v", list)
	}
	id := strconv.FormatInt(list[0].ID, 10)
	if code, stdout, _ := exec(t, "cert", "ls"); code != 0 || !strings.Contains(stdout, "*.corp.test") || !strings.Contains(stdout, "(not public)") {
		t.Fatalf("table: %d\n%s", code, stdout)
	}
	if code, _, stderr := exec(t, "certificate", "remove", id); code != int(output.ExitUsage) || !strings.Contains(stderr, "--yes") {
		t.Fatalf("remove without --yes off a terminal: %d %s", code, stderr)
	}
	ok[map[string]int64](t, "certificate", "remove", id, "--yes")
	if code, _, stderr := exec(t, "certificate", "remove", id, "-y"); code == 0 || !strings.Contains(stderr, "not found") {
		t.Fatalf("remove again: %d %s", code, stderr)
	}
}
