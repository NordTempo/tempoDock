package acmedns

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// waitEvery is how often WaitTXT asks the name servers again. It only
// runs while a certificate is being issued.
const waitEvery = 2 * time.Second

// WaitTXT returns once every one of zone's authoritative name servers
// answers name's TXT lookup with all of values, the way a DNS.Wait checks
// what the CA will see. It asks each server directly (not a caching
// resolver, which may hold an answer from before the records), every two
// seconds, until ctx ends.
func WaitTXT(ctx context.Context, zone, name string, values []string) error {
	nss, err := net.DefaultResolver.LookupNS(ctx, zone)
	if err != nil {
		return fmt.Errorf("find the name servers of %s: %w", zone, err)
	}
	if len(nss) == 0 {
		return fmt.Errorf("%s has no name servers", zone)
	}
	var last error
	for {
		last = nil
		for _, ns := range nss {
			if err := hasTXT(ctx, strings.TrimSuffix(ns.Host, "."), name, values); err != nil {
				last = err
				break
			}
		}
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the TXT records at %s didn't appear in time: %w", name, last)
		case <-time.After(waitEvery):
		}
	}
}

// hasTXT asks the name server ns for name's TXT records and checks that
// they include values.
func hasTXT(ctx context.Context, ns, name string, values []string) error {
	var d net.Dialer
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return d.DialContext(ctx, network, net.JoinHostPort(ns, "53"))
	}}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, err := r.LookupTXT(qctx, name)
	if err != nil {
		return fmt.Errorf("%s: %w", ns, err)
	}
	for _, v := range values {
		if !slices.Contains(got, v) {
			return fmt.Errorf("%s doesn't serve the record yet", ns)
		}
	}
	return nil
}
