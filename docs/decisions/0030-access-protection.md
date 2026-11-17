# 0030: Access protection for a service's domains

Status: accepted (decided by the agent, on Saile's direction)

## Context

Every domain is public. Staging environments, previews and internal tools
should not be: Saile asked for a way to keep a deployment off the public
internet, with a password or with the Dock login, chosen per service.

| Force | Means here |
|---|---|
| Whole service | A service has several hostnames (custom, generated, `www`). Protecting one while the others stay open is a false sense of safety |
| No app changes | Protection must work for any image, without code in the app |
| Idle means idle | No polling; a public service must cost exactly what it did |
| Hot path | A protected request is checked on the server that serves it; it never crosses to the control plane |
| Nodes alone | A node keeps serving when the control plane is away ([0014](0014-node-protocol.md)) |
| Strict session cookie | Dock's session cookie is `SameSite=Strict`, so a cross-site navigation to Dock arrives without it |
| Stock Caddy | Caddy is the release binary ([0004](0004-caddy-as-separate-proxy.md)); no plugins |

## Decision

| Topic | Decision |
|---|---|
| Scope | One setting per service (a service lives in one environment), covering all its domains. Redirect domains only redirect. `public` by default |
| Modes | `public`; `password`: HTTP basic auth with one username and password; `login`: members signed in to Dock who can read the service's environment |
| Storage | `service_access` (no row: public): mode, username, bcrypt hash, and for login a 32-byte key sealed with the secrets box |
| Password | Caddy's `authentication` handler with `http_basic`, bcrypt cost 6, `hash_cache` on. At least 12 characters. An empty password on a password-protected service keeps the current one |
| Login, at the proxy | Caddy's `forward_auth`, written out in JSON: before every request it asks the server's access check (`GET /internal/caddy/access` on the loopback internal listener, a GET without the body), sending the route's key and login URL as headers it sets itself. A 2xx lets the request on, with `X-Dock-User` set to the visitor and the check's headers removed; any other answer goes back to the client |
| Login, the check | `caddy.AccessHandler`, stateless: a valid session cookie (`dock_access`, HMAC-signed with the key, host-bound, 12 hours) answers 204 and the visitor. Without one, a page load is redirected to Dock's `/access?host=&to=`, anything else gets a 401 |
| Login, the handshake | Dock's `/access` page (signed in, or after logging in) asks `POST /access/grants` for a grant: a one-minute token for that host naming the user. The browser goes to `https://<host>/.dock/access?grant=…&to=…`; the check verifies it, sets the session cookie on the app's own hostname and redirects to `to` (a local path only) |
| Who gets in | Read on the service's environment, in the service's org. A member of several orgs gets in whichever org they act in |
| Identity | `X-Dock-User` carries the Dock user's email (login) or the basic-auth username (password), replacing any value the client sent |
| Revoking | Turning login off and on again makes a new key, which signs everyone out. Disabling a user stops new grants; their cookie lasts until it expires (at most 12 hours) |
| Nodes | Routes carry the access setting over the link from protocol 5. Login mode is refused for a server whose node is older; a node never gets protected routes it would serve openly |
| Dock's address | The login URL is Dock's public address ([0029](0029-public-address.md)), or its private one without it. Changing the address reroutes every online server |
| API, CLI, UI | `GET`/`PUT .../services/{service}/access`, `POST /access/grants`; `dock access`, `dock access public\|password\|login`; on the Domains tab, a line saying who can open the service, with Change (mock C, picked by Saile). Audited as `service.access` |

## Amendment: uptime probes (2026-11-17)

An uptime check ([0055](0055-uptime-checks.md)) of a protected
domain got the protection's 401, so every such service showed as down and
raised an alert. Dock's own probe now passes the protection, by the path
the access check already has: a session cookie.

| Topic | Decision |
|---|---|
| What the probe carries | `dock_access`, a session signed with the service's key like a visitor's (`access.ProbeCookie`). No new token kind, header secret or allowlist. For a login the access check is unchanged, so nodes of any version let it through; for a password, see below |
| Scope | Bound to the probed hostname, as every session is, and valid for one minute (`access.ProbeTTL`), not 12 hours. A new one is signed for each probe (opening the key and one HMAC, microseconds) and none is stored |
| Who it names | `dock-uptime` (`access.ProbeUser`), which the app gets in `X-Dock-User`. A person's session names their email address, so the two can't be confused. It is no member: nothing is audited as a sign-in, and the probe's `User-Agent: dock-uptime/*` already keeps it out of traces and sleep's access log |
| Which checks | Only a check whose host is a verified, serving (not redirecting) domain of the check's own service, and only while that service is protected. The scheduler matches hostname and service when it loads its checks (`ListProtectedDomains`); changing a service's access reloads them. Any other check is probed as before, without a cookie |
| Redirects | The probe follows none (`http.ErrUseLastResponse`), so the cookie reaches the probed host only. Signed with that service's key for that hostname, it would open nothing anywhere else either |
| Where it runs | On the control plane only. The cookie is signed there and never crosses the node link (`remote.Client.Probe` refuses a request with one), so a protected check loses [0055](0055-uptime-checks.md)'s "from another server" and no protocol change is needed. Sending it to a probing node is a possible follow-up |
| Password mode | Dock holds only the password's hash and can't answer basic auth, and expecting 401 would only prove that the gate answers. So the password's route also passes the probe's session, and nothing else: a request with `User-Agent: dock-uptime/*` and a `dock_access` cookie goes to the access check instead of basic auth (a subroute; everyone else meets basic auth as before, at no extra cost), and the check, asked with `X-Dock-Access-Probe`, answers 2xx only for a valid session naming `dock-uptime`. A visitor's session, a grant at the callback or anything else gets its 401 |
| Password mode, the key | A probe key per service, derived from the secrets key (`Box.Tag`) and the service's ID: nothing stored, no migration. It rides the route as `node.Access.ProbeKey`, an optional field on the node link. A node older than this ignores it and keeps asking the probe for the password, so its checks answer 401 until it is updated |

## Why this shape

**Per service, not per domain.** Per domain would let a service be
protected on its custom hostname and open on its generated one. One
setting per service has no such gap, and each environment is its own
service, so "staging is private, production is public" is one switch.

**Basic auth for passwords.** It is built into Caddy, costs the daemon
nothing, works the same on every server and behind tunnels, and scripts
can use it (`curl -u`). A login form would need its own pages and session
handling for a weaker version of the login mode. bcrypt at cost 6 (about
3 ms) is the trade-off: Caddy pays for every wrong guess, on a server the
apps share, and a 12-character password is out of reach of online
guessing at any cost.

**A stateless check on the serving server.** Stock Caddy can't verify a
signed cookie, so `forward_auth` asks a small handler. Putting the key in
the route (Caddy sends it along) means the handler needs no state: it
works on a node right after a restart and while the control plane is
away, and costs a few microseconds plus a loopback round trip
(`BenchmarkAccessHandler`). The key also sits in Caddy's saved config,
which only Caddy and root read, like the bcrypt hashes.

**A page in Dock for the handshake.** Dock's session cookie is
`SameSite=Strict`, so the browser's redirect from the app arrives without
it. A page in the UI can ask the API with the cookie once loaded, and it
reuses the login page's "log in and come back".

**Grants and sessions are HMAC tokens.** The control plane signs a grant,
the server turns it into a session, both with the service's key; no
database is read on a request, and nothing on the node has to be kept.

## Consequences

- A protected service's every request adds Caddy's call to the access
  check (login) or a cached password check (password). Public services
  are unchanged.
- Cookies are per hostname: signing in to `staging.example.com` doesn't
  sign you in to `admin.example.com`; each goes through `/access` once
  (no clicks when you are signed in to Dock).
- A script calling a login-protected API gets 401 with no cookie. Use a
  password, or a public service, for machine traffic; a bypass token for
  automation is a possible follow-up.
- An app path `/.dock/access` is shadowed on login-protected services.
- Webhooks to a protected service need the password in the URL, or a
  public service in front.
