# 0042: Custom domains are verified with a TXT record

Status: accepted (security hardening, 2026-11-03).

## Context

Any account can have an org ([0027](0027-organizations.md)), and an org
could add any hostname to a service, first come, first served. Dock then
routed it and had Caddy ask for its certificate. So an org could squat a
hostname another org, or its real owner, would want: the name was taken
in Dock, and its traffic would route to the squatter's service the moment
DNS pointed at the server. 0027 left "custom domain verification" to the
hosted phase; open orgs make it needed now.

## Decision

| Topic | Decision |
|---|---|
| Proof | A custom domain gets a random token when it is added. The org proves it controls the hostname with `TXT _dock-challenge.<hostname>` = `dock-verify=<token>`. The API returns it as `verification` with `verified`; the CLI, UI and MCP tools show it |
| Until verified | The domain is listed, but not routed (`ListRoutableDomains`), gets no certificate (`AllowCertificate`), counts no traffic and carries no analytics site over. Its status is `waiting_for_dns`, with the record to add as the reason. Routing records (A, CNAME) can be set up at the same time |
| No proof needed | Generated hostnames and any under the server's base domain (the platform's names); domains from Cloudflare (Dock makes their records in a zone an operator connected, [0036](0036-cloudflare-api.md)); and domains an operator adds (they run the proxy anyway). These are verified when added |
| Who holds a hostname | At most one verified claim per hostname (a partial unique index). Unverified claims don't block: several orgs may claim one, and the first to verify holds it. A new claim on a verified hostname is refused (`hostname_taken`); within one org a hostname still belongs to one service |
| The others are told | When a claim verifies, every other claim on the hostname gets status `error` ("another organization proved it controls ..."), an audit event `domain.lost` in its project and a live event; the domain error raises its alert. Removing the verified domain lets the others verify again |
| Checking | The existing domain checker (5 s backing off to 5 min) looks the record up with every check of an unverified domain; no new timer. `POST .../domains/{hostname}/check` (`dock domain check`, "Check now", `check_domain`) checks at once. Lookups use the system resolver from the control plane; `Options.LookupTXT` replaces it in tests |
| Once verified | Verified stays verified: removing the TXT record later changes nothing |
| Existing domains | Migration 0041 marks every domain verified as of when it was added, so nothing live stops |

## Consequences

- Squatting a custom hostname takes the hostname's DNS, the same proof
  any other platform asks for.
- Names under the base domain and in operators' Cloudflare zones stay
  first come, first served across orgs: they are the platform's, and
  an operator decides who may use them.
- The public hostname a service's variables can reference may be an
  unverified domain, as it may be one still waiting for DNS.
- `domains.hostname` is no longer unique by itself. Lookups by hostname
  that mean "the domain that routes" (`GetDomainByHostname`) read the
  verified claim; a service's own claim is `GetServiceDomain`.
