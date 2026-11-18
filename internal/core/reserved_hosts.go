package core

import (
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// reservedBaseLabels are the names directly under a server's base domain
// that an organization can't take for a service or a status page: ones
// Dock uses or may use for itself, and ones browsers, mail servers and
// certificate authorities give a meaning to on any domain. Everything
// under a base domain is trusted as the platform's (needsProof asks no
// TXT record there), so whoever held one of these could pass for the
// platform. Generated hostnames end in a random part (ADR 0087) and never
// match. Operators may still use them: the base domain is theirs.
//
// Checked when a hostname is added or changed (reservedHostErr), never
// for one already held, so nothing that works today stops.
var reservedBaseLabels = map[string]bool{
	// Dock's own and likely addresses.
	"dock": true, "console": true, "registry": true, "api": true, "admin": true,
	"hooks": true, "status": true, "stats": true, "analytics": true, "tempobase": true,
	"auth": true, "login": true, "sso": true, "git": true, "preview": true, "previews": true,
	// The web and its conventions.
	"www": true, "localhost": true, "wpad": true, "isatap": true,
	// Mail.
	"mail": true, "smtp": true, "imap": true, "pop": true, "mx": true, "mta-sts": true,
	"autoconfig": true, "autodiscover": true,
	// DNS and certificates. The underscore names aren't hostnames today
	// (normalizeHostname refuses them); they are here so they stay
	// refused if that rule ever loosens.
	"ns": true, "ns1": true, "ns2": true, "_acme-challenge": true, "_dmarc": true, "_domainkey": true, "_mta-sts": true,
}

// reservedBases are the base domains a hostname chosen for a service on a
// server with ingress ing is checked under (reservedHostErr): the
// server's and the platform's.
func (c *Core) reservedBases(ing serverIngress) []string {
	return []string{ing.baseDomain, strings.TrimPrefix(c.cfg.BaseDomain, "*.")}
}

// reservedHostErr refuses host for actor a when it is a reserved name
// directly under one of bases (base domains, "" ignored): the single
// check for every path that binds a name someone chose (AddDomain, a
// project file's plan, a status page's hostname). Operators pass.
func reservedHostErr(a Actor, host string, bases ...string) error {
	if a.Operator || a.userPlatform == apitypes.PlatformOperator {
		return nil
	}
	label, rest, ok := strings.Cut(host, ".")
	if !ok || !reservedBaseLabels[label] {
		return nil
	}
	for _, base := range bases {
		if base != "" && rest == base {
			return errf(apitypes.CodeHostnameTaken, "%s is reserved for the platform under %s; pick another name, like %s-app.%s", host, base, label, base)
		}
	}
	return nil
}
