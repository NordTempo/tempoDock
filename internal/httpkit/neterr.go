package httpkit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"syscall"
	"time"
)

// NetErrorText says in plain, fixed words why a request to another host
// got no answer ("DNS: no such host", "connection refused", "TLS: the
// certificate isn't for this hostname", "no answer within 5s"), or ""
// when err isn't a network error. It never passes on the raw error: dial
// and TLS errors name addresses and certificate names of whatever
// answered, which a check's owner must not learn. timeout is the
// request's limit, said with a timeout; 0 leaves it out.
//
// Uptime probes (internal/node/uptime) and core's one-line errors
// (errorLine) use it, so a network failure reads the same everywhere.
func NetErrorText(err error, timeout time.Duration) string {
	if d, ok := errors.AsType[*net.DNSError](err); ok {
		switch {
		case d.IsNotFound:
			return "DNS: no such host"
		case d.IsTimeout:
			return "DNS: the lookup timed out"
		}
		return "DNS: the lookup failed"
	}
	timedOut := errors.Is(err, context.DeadlineExceeded)
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		timedOut = true
	}
	if timedOut {
		if timeout > 0 {
			return "no answer within " + timeout.String()
		}
		return "no answer in time"
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "no route to the host"
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return "TLS: the certificate isn't for this hostname"
	}
	if ce, ok := errors.AsType[x509.CertificateInvalidError](err); ok && ce.Reason == x509.Expired {
		return "TLS: the certificate has expired or isn't valid yet"
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return "TLS: the certificate isn't signed by a trusted authority"
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return "TLS: the certificate is invalid"
	}
	if _, ok := errors.AsType[tls.AlertError](err); ok {
		return "TLS: the handshake failed"
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return "TLS: the handshake failed (is it plain HTTP?)"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "the connection closed before an answer"
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return "the connection failed"
	}
	return ""
}
