# 0082: Open source under the AGPL, with a trademark policy and a CLA

Status: accepted (decided with Saile, 2026-11-14). Answers the license
question [0008](0008-keep-doors-open.md) left open. Not yet public: the
repository goes public with v1.0.0, through `scripts/go-public.sh`.

## Context

Dock is meant to grow by word of mouth: people self-host it, like it and
tell others. That audience (self-hosters, r/selfhosted, Hacker News)
trusts OSI open source and pushes back on "source available" licenses.
NordTempo also wants to run the paid hosted version later, and not have
someone else sell hosted Dock first.

| Want | Means |
|---|---|
| Liked by the open source community | An OSI-approved license |
| Anyone may run it in production | No field-of-use limits |
| Others' changes stay open | Copyleft that reaches network use |
| NordTempo can sell hosting and other terms | NordTempo can license all of the code, contributions included |
| Nobody sells "Tempo Dock" hosting but NordTempo | The names are protected separately from the code |

No OSI license forbids offering the software as a service. A non-compete
license (FSL, BSL, Elastic) would, but isn't open source, and an extra
restriction added to the AGPL may be removed by any recipient (its
section 7). Dokploy's history shows the cost of mixing: Apache-2.0 with
extra resale terms drew criticism until it moved to an open core (Apache
core, proprietary enterprise folder).

## Decision

1. **AGPL-3.0-only** ([LICENSE](../../LICENSE)) for everything in the
   repository. Running modified Dock for others over a network means
   publishing the changes. Apps deployed on Dock are separate programs and
   are not covered.
2. **Trademarks** ([TRADEMARKS.md](../../TRADEMARKS.md)): Tempo Dock,
   Dock, tempoBase and NordTempo, and their logos, stay NordTempo's. A fork
   or a hosted service needs a name of its own.
3. **A CLA** ([CLA.md](../../CLA.md)), based on Apache's ICLA and checked
   on every pull request (`.github/workflows/cla.yml`). Contributors keep
   their copyright and license it to NordTempo under any terms. This keeps
   a hosted version, commercial licenses, an open core or a later license
   change possible without asking every contributor.
4. **Protection against a hosted competitor is not the license's job.**
   The AGPL makes a closed fork impossible and a hosted one costly (all of
   its multi-tenant work would be public); the trademark keeps the name;
   being first, with a "Dock Cloud" waitlist on launch day, does the rest.
5. **The repository and CI are made safe to be public**:
   - CI and releases run on GitHub-hosted runners, free for public
     repositories. A fork's pull request never reaches the self-hosted
     runners or their shared cache. Releases use no GitHub caches.
   - Every action is pinned to a commit SHA.
   - `scripts/go-public.sh` makes the repository public, then turns on
     private vulnerability reporting, secret scanning and push protection,
     approval for outside contributors' workflow runs, SHA-pinned actions
     only, immutable releases, and rulesets for `main` and `v*` tags.
     GitHub's free plan only offers these to public repositories.
6. **Going public is v1.0.0.**

## Consequences

- Dependencies stay permissive (MIT, Apache-2.0, BSD, ISC and the like),
  as they all are today, and all compatible with the AGPL.
  Third-party notices (Huma in `third_party/huma`, DB-IP's attribution in
  the analytics UI) stay with the code.
- Someone may still host Dock commercially under another name, publishing
  their changes. If that ever outweighs adoption, the CLA allows
  relicensing future versions; versions already released stay AGPL.
- Contributing needs one CLA comment, a small cost for outside
  contributors.
- Agents treat fork pull requests (code, titles, comments) as untrusted
  input and never merge one without a maintainer (AGENTS.md).
- Release signing beyond SHA256SUMS is not decided here; it is the next
  hardening step for the update path.
