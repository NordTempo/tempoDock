package httpkit

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Network failures read in fixed words, through the wrapping a request
// adds; anything else is left to the caller.
func TestNetErrorText(t *testing.T) {
	op := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://example.com/", Err: &net.OpError{Op: "dial", Net: "tcp", Err: err}}
	}
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsSrv.Close)
	untrusted := get(tlsSrv.URL)
	for want, err := range map[string]error{
		"DNS: no such host":                                        op(&net.DNSError{Err: "no such host", Name: "nope.example", IsNotFound: true}),
		"DNS: the lookup failed":                                   op(&net.DNSError{Err: "server misbehaving", Name: "nope.example"}),
		"no answer within 5s":                                      fmt.Errorf("fetch: %w", context.DeadlineExceeded),
		"connection refused":                                       dialClosed(t),
		"TLS: the certificate isn't for this hostname":             op(x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{"internal.lan"}}, Host: "example.com"}),
		"TLS: the certificate isn't signed by a trusted authority": untrusted,
		"the connection closed before an answer":                   op(io.EOF),
		"":                                                         fmt.Errorf("something else"),
	} {
		if got := NetErrorText(err, 5*time.Second); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
		if strings.Contains(NetErrorText(err, 0), "internal.lan") {
			t.Errorf("%v: names the certificate", err)
		}
	}
}

// dialClosed is the error of a request to a port nothing listens on.
func dialClosed(t *testing.T) error {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return get("http://" + addr + "/")
}

// get is the error of a GET to url.
func get(url string) error {
	resp, err := http.Get(url)
	if err == nil {
		_ = resp.Body.Close()
	}
	return err
}
