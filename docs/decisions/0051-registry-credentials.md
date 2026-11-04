# 0051: Pull credentials for private registries

Status: accepted (2026-11-03). Amends [0014](0014-node-protocol.md) (protocol
18) and [0017](0017-mcp-endpoint.md) (one read-only tool).

## Context

A deploy pulls its image on the release's server. Pulls from Dock's own
registry carry a short-lived token the control plane mints (0014 section
4); every other pull was anonymous. `dock deploy --image
ghcr.io/acme/private:1` failed, and the guide said private registries were
not implemented. CI builds images into private registries as a rule, so
this blocked the image path ([0006](0006-every-way-to-build.md)) for most
real projects.

| Force | Means here |
|---|---|
| Secrets stay sealed | Like variables and storage keys: sealed with the install's key, write-only after saving, never in the API, MCP, events, logs or audit |
| One shape | An org setting next to org variables, managed by its admins, with CLI and UI parity ([0026](0026-ui-cli-parity.md)) |
| Remote servers | The password travels over the node link to the server that pulls; an older node must not silently pull anonymously |
| Cheap | One indexed query per pull; nothing when no pull happens |

## Decision

| Topic | Decision |
|---|---|
| Table | `registry_credentials` (migration 0054): `org_id`, `host`, `username`, sealed `secret`, created and updated at and by. Primary key `(org_id, host)`, `WITHOUT ROWID`: the pull's lookup and the list's order. Deleted with the org |
| Sealed | `secret` is sealed with the secrets box bound to its row ([0041](0041-sealed-values-bound-to-their-place.md)): the AAD is `registry_credentials.secret` with the org ID and host, the row's natural key, and the column is in `sealedColumns`. A secret pasted into another org's or host's row doesn't open there |
| Hosts | Stored lower case. A host is valid when Docker reads it as the registry of an image under it (`github.com/distribution/reference`): `ghcr.io`, `registry.example.com:5000`, `localhost:5000`, `docker.io`; no scheme, no path, nothing Docker would read as a Docker Hub namespace (`acme`). `index.docker.io` and `registry-1.docker.io` are stored as `docker.io`. Dock's own registry host is refused: it signs in with its own tokens |
| Picking one | `(*deployment).pull`: an image in Dock's registry gets its token, as before, and never a credential. Any other ref is parsed the way Docker does (`ParseNormalizedNamed`), so `nginx` and `acme/web` are Docker Hub, and the org's credential for that host (the project's org, never another's) goes into `node.PullOptions{Username, Password}`. Every pull of a release goes there: image deploys, redeploys, and rollbacks or promotions whose image is gone from the server. Template services pull public images and need nothing; with Docker Hub credentials they use them too, which helps with Hub's rate limits |
| Docker | `X-Registry-Auth` with username, password and `ServerAddress` (the ref's registry; `https://index.docker.io/v1/` for Docker Hub, as `docker login` records it). Docker then uses basic auth or the registry's token endpoint, whichever it asks for |
| Node link | Protocol 18: `pull` takes optional `username` and `password`. `Client.Pull` checks the protocol only when credentials are set, so an older node answers anonymous pulls as before and refuses a credentialed one with "upgrade dock on it" instead of pulling anonymously |
| Access | List, set and remove need an unlimited admin of the org (`requireUnlimited(admin)`, like org variables). Audit actions `org.registry_credential_set` and `org.registry_credential_remove`, target the host; the summary names the username, never the secret |
| Surfaces | API `GET /org/registry-credentials`, `PUT` and `DELETE /org/registry-credentials/{host}`; `dock registry login HOST --username NAME [--password-stdin]` (a terminal prompt otherwise; never an argument), `dock registry logout HOST`, `dock registry credentials`; Settings › Registries in the UI, refetched from the audit events; MCP `list_registry_credentials` (read-only, hosts and usernames). No MCP tool takes or returns a password: it would put it in an agent's context |
| Environment lock | Outside it ([0044](0044-environment-lock.md)). The lock guards changes inside an environment (`checkEnv`); a credential is an org-level admin setting, not an environment change, like org variables and storage connections, which take no lock either. Only an unlimited admin token can change one, and the lock isn't a boundary against those (0044) |
| Errors | A pull the registry refuses (unauthorized, denied, authentication required, forbidden) says what to run: `dock registry login HOST` without a credential, a check or replacement of it with one |
| Not now | Builds pulling private base images in a Dockerfile (BuildKit needs the credentials as a session attachable on the builder; a separate decision), credential helpers and cloud IAM (ECR, GCR's short-lived tokens: use a long-lived key for now), per-project or per-environment credentials, checking a credential against the registry on save |

## Consequences

- `dock deploy --image ghcr.io/acme/private:1` works once an admin ran
  `dock registry login ghcr.io` for the org. CI only needs `DOCK_TOKEN`; the
  registry token stays in Dock.
- Credentials are in the control plane's database and backups, sealed like
  every secret, and are sent to a server for each pull that needs them; the
  node keeps nothing (Docker gets them per pull and doesn't store them).
- A remote server pulling private images must run protocol 18 or newer.
- Members who can read the audit log see which hosts and usernames were
  set, as they see org variable names; the list itself is for admins.
