//go:build integration

package acmedns_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/acmedns"
)

// Pebble, Let's Encrypt's ACME test server, and its challenge test
// server, which answers Pebble's DNS queries with the TXT records a test
// sets through its management API.
const (
	pebbleImage    = "ghcr.io/letsencrypt/pebble:2.10.1@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"
	challtestImage = "ghcr.io/letsencrypt/pebble-challtestsrv:2.10.1@sha256:12ce21884def456bcf9786542113949e1f19dc7738d2c70e156c2d0c38a1405b"
)

// TestObtainFromPebble issues a wildcard certificate end to end: account,
// order, both DNS-01 challenges through the DNS, the chain verified
// against Pebble's root, and the records cleaned up.
func TestObtainFromPebble(t *testing.T) {
	p := startPebble(t)
	dns := &challtestDNS{api: p.challtest, present: map[string][]string{}}
	key, _, err := acmedns.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	req := acmedns.Request{Directory: p.directory, AccountKey: key, Email: "ops@example.com", Domain: "apps.example.com", DNS: dns, HTTPClient: p.client}
	cert, err := acmedns.Obtain(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	pair, err := tls.X509KeyPair(cert.ChainPEM, cert.KeyPEM)
	if err != nil {
		t.Fatalf("chain and key don't pair: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(p.root(t))
	inter := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		inter.AddCert(c)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"apps.example.com", "web-shop-k7m2q9.apps.example.com"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots, Intermediates: inter}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !cert.NotAfter.Equal(leaf.NotAfter) || !cert.NotBefore.Equal(leaf.NotBefore) {
		t.Errorf("validity %v-%v, leaf says %v-%v", cert.NotBefore, cert.NotAfter, leaf.NotBefore, leaf.NotAfter)
	}
	if got := dns.waited; len(got) != 2 {
		t.Errorf("waited for %v, want both challenges' values at once", got)
	}
	if len(dns.present) != 0 {
		t.Errorf("records left behind: %v", dns.present)
	}

	// The same account orders again (a renewal): registering finds it.
	if _, err := acmedns.Obtain(ctx, req); err != nil {
		t.Fatalf("renewal: %v", err)
	}
}

// TestObtainCleansUpWhenRefused: a challenge whose record never appears
// fails the order, and the records are removed anyway.
func TestObtainCleansUpWhenRefused(t *testing.T) {
	p := startPebble(t)
	dns := &challtestDNS{api: p.challtest, present: map[string][]string{}, wrong: true}
	key, _, err := acmedns.NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	_, err = acmedns.Obtain(ctx, acmedns.Request{Directory: p.directory, AccountKey: key, Domain: "apps.example.com", DNS: dns, HTTPClient: p.client})
	if err == nil || !strings.Contains(err.Error(), "didn't accept the TXT record") {
		t.Fatalf("err = %v, want the CA's refusal", err)
	}
	if len(dns.present) != 0 {
		t.Errorf("records left behind: %v", dns.present)
	}
}

// challtestDNS is a DNS that publishes records through pebble-challtestsrv.
type challtestDNS struct {
	api string
	// wrong publishes a value the CA doesn't expect.
	wrong bool

	mu      sync.Mutex
	present map[string][]string
	waited  []string
}

func (d *challtestDNS) Present(ctx context.Context, name, value string) error {
	if d.wrong {
		value = "not-" + value
	}
	if err := d.call(ctx, "/set-txt", map[string]string{"host": name + ".", "value": value}); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.present[name] = append(d.present[name], value)
	return nil
}

func (d *challtestDNS) Wait(_ context.Context, _ string, values []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.waited = values
	return nil // challtestsrv answers at once
}

func (d *challtestDNS) CleanUp(ctx context.Context, name string) error {
	if err := d.call(ctx, "/clear-txt", map[string]string{"host": name + "."}); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.present, name)
	return nil
}

func (d *challtestDNS) call(ctx context.Context, path string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.api+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("challtestsrv %s: %s", path, resp.Status)
	}
	return nil
}

// pebble is a running Pebble and its challenge server.
type pebble struct {
	directory, management, challtest string
	// client trusts Pebble's API: its test certificate, for localhost.
	client *http.Client
}

// root is Pebble's root CA, which issued the chains it hands out.
func (p pebble) root(t *testing.T) *x509.Certificate {
	t.Helper()
	resp, err := p.client.Get(p.management + "/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	b, _ := pem.Decode(body)
	if b == nil {
		t.Fatalf("no root from Pebble: %s", body)
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// startPebble runs Pebble and challtestsrv on Docker's default bridge,
// Pebble resolving through challtestsrv, with their ports published on
// the bridge's gateway, and waits for both.
func startPebble(t *testing.T) pebble {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	for _, img := range []string{challtestImage, pebbleImage} {
		docker(t, "pull", "-q", img) // first, so run's output is only the ID
	}
	gw := strings.TrimSpace(docker(t, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"))
	chall := strings.TrimSpace(docker(t, "run", "-d", "--rm", "-p", gw+"::8055", challtestImage,
		"-defaultIPv4", "", "-defaultIPv6", "", "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", ""))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", chall).Run() })
	challIP := strings.TrimSpace(docker(t, "inspect", chall, "--format", "{{.NetworkSettings.Networks.bridge.IPAddress}}"))
	peb := strings.TrimSpace(docker(t, "run", "-d", "--rm", "-p", gw+"::14000", "-p", gw+"::15000",
		"-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "-e", "PEBBLE_AUTHZREUSE=0",
		pebbleImage, "-config", "/test/config/pebble-config.json", "-dnsserver", net.JoinHostPort(challIP, "8053")))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", peb).Run() })

	port := func(id, p string) string {
		out := strings.TrimSpace(docker(t, "port", id, p))
		_, port, err := net.SplitHostPort(strings.Fields(out)[0])
		if err != nil {
			t.Fatalf("port %s of %s: %q", p, id, out)
		}
		return port
	}
	p := pebble{
		directory:  "https://" + net.JoinHostPort(gw, port(peb, "14000")) + "/dir",
		management: "https://" + net.JoinHostPort(gw, port(peb, "15000")),
		challtest:  "http://" + net.JoinHostPort(gw, port(chall, "8055")),
		client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pebbleCert(t, peb), ServerName: "localhost"},
		}},
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		resp, err := p.client.Get(p.directory)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Pebble didn't start: %v\n%s", err, docker(t, "logs", peb))
		}
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		resp, err := http.Post(p.challtest+"/clear-txt", "application/json", strings.NewReader(`{"host":"warmup."}`))
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("challtestsrv didn't start: %v", err)
		}
	}
	return p
}

// pebbleCert is a pool holding the certificate Pebble's API serves (the
// image's test one, for localhost), as its own anchor.
func pebbleCert(t *testing.T, container string) *x509.CertPool {
	t.Helper()
	out := docker(t, "cp", container+":/test/certs/localhost/cert.pem", "-")
	// docker cp to stdout writes a tar of the one file.
	start := strings.Index(out, "-----BEGIN CERTIFICATE-----")
	end := strings.Index(out, "-----END CERTIFICATE-----")
	pool := x509.NewCertPool()
	if start < 0 || end < start || !pool.AppendCertsFromPEM([]byte(out[start:end+len("-----END CERTIFICATE-----")])) {
		t.Fatalf("no certificate in Pebble's image: %q", out)
	}
	return pool
}

// docker runs the docker CLI and returns its standard output.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s%s", strings.Join(args, " "), err, out, stderr.String())
	}
	return string(out)
}
