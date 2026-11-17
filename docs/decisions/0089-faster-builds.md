# 0089: Faster builds: more cores, zstd layers, pinned Railpack images, cold previews

Status: accepted (asked by Saile through the coordinator, 2026-11-17).
Amends [0006](0006-every-way-to-build.md): the builder's CPU cap.

## Context

Builds are what people wait on most. A one-line change to a Next.js 16 site
(detoo, on prod) took about two minutes, 78 s of it `next build`:
Turbopack compiling the whole app again "using 1 worker", then 31 s of
TypeScript. Measured on the dev VM with Dock's own builder, its own
BuildKit and a registry on loopback (`TestBuildTimes`,
`e2e/apps/nextjs-pnpm`: pnpm, Next.js 16.4, Tailwind 4, 44 pages,
standalone output, the Next.js example Dockerfile), with the old
defaults (one core, 2 GB):

| Step | Dockerfile, cold | Dockerfile, warm | Railpack, cold | Railpack, warm |
|---|---|---|---|---|
| Source pack and upload | 2 ms (13 KB) | 2 ms | 2 ms | 2 ms |
| Pulling or resolving Railpack's images | | | ~20 s | 1 to 15 s |
| Dependency install | 13.4 s | cached | 17.1 s, after 9 s of apt and mise | cached |
| `next build` | 20.6 s | 20.9 s | 18.5 s | 8.1 s (its cache mount) |
| Export into Docker | 3.5 s | 4.1 s | 26.0 s | 4.5 s |
| Push to the registry | 0.4 s | 0.3 s | 0.7 s | 0.2 s |
| Start to the first 200 | 0.8 s | 1.1 s | 1.2 s | 0.8 s |
| **Build and push** | **59.8 s** | **35.4 s** | **106.5 s** | **35.3 s** |

What stands out:

- **The builder had one core.** `[buildkit] cpus` defaulted to 1, and
  Turbopack, TypeScript, pnpm and layer compression scale with cores: a
  warm Dockerfile build takes 35.4 s on one core, 23.2 s on two, 20.2 s
  on four. Apps were already protected without the cap: the BuildKit
  container runs at a quarter of the normal CPU weight, so an app that
  wants the CPU gets it.
- **Dockerfile builds of Next.js apps keep no compiler cache.** The
  layer cache works (the install step is `CACHED` when the lockfile is
  unchanged), but the step that runs `next build` copies changed source,
  so it runs again, and without a cache mount on `.next/cache` Turbopack
  starts from nothing: 9.3 s to compile against 1.0 s with the cache.
  Railpack builds already mount it.
- **Railpack resolved its images on every build.** Its frontend,
  builder and runtime images are named by tag, so each build asked
  ghcr.io what they point at: under a second on a good day, 15 s of a
  35 s warm build on a bad one, and a failed build when ghcr.io is down.
- **Layers were gzipped,** single-threaded per layer: 26 s of a cold
  Railpack build (its image carries node_modules and mise).
- **Upload, push, start and health check are small** next to the build.

## Decision

**`[buildkit] cpus = 0` is the default: half the server's cores, but at
least two (all of a smaller server).** The memory cap stays the hard
limit; the CPU weight stays a quarter of an app's. At least two because
one core to two is most of the gain and small servers are the common
case (detoo's server runs Next.js with one worker); half on larger ones
so a build can't take the whole server when something else (the daemon,
a backup, a scan) starts. `install.sh` writes `cpus = 0`. An existing
config keeps what it says, which for installs before this is `cpus = 1`:
the release notes say to change it.

**Layers are exported with zstd.** About three times faster than gzip
(the cold Railpack export 15.6 s to 5.2 s on two cores).
Docker reads zstd layers since 23 (Dock needs 28). Layers that already
have a blob keep it, base images' and earlier builds' gzip ones
included, so an upgrade doesn't push every layer again.

**Railpack's images are pinned by digest** (`railpackImages`), through
a BuildKit source policy for the images its plans name and the digest in
the frontend reference. The tags are versioned with Railpack, so the
pins change when Railpack does, and a test fails until they are updated.
The `alpine:latest` Railpack hashes secrets in is pinned too: a helper,
nothing of it reaches the image.

**Dock says when a Dockerfile keeps no `.next/cache`.** When the
source's package.json depends on `next` and no `RUN` step mounts a cache
on `.next/cache`, the build log gets one hint line with the mount to add,
targeted at the `WORKDIR` of the step that builds. Dock doesn't add the
mount itself: a cache mount isn't part of the image, so a Dockerfile that
copies `.next` into the runtime stage would lose what Next.js wrote to
`.next/cache` at build time (its fetch cache), silently changing what the
user wrote. The guide's "Build caches" section lists the mounts for
Next.js, pnpm, npm, Yarn, pip and Go.

**Cache mounts stay per environment, and previews start empty.** Sharing
the base environment's cache scope with its previews would warm their
first build, but cache mounts are mutable state, and a pull request's
code is untrusted (ADR 0056): it could plant a changed package store or
compiler cache that the next production build runs. Sharing one scope
among the previews of an environment has the same problem across pull
requests: a fork's could poison a preview that holds copied secrets.
Layers are content-addressed and shared by every build on a server, so a
preview's install step is still `CACHED` when its lockfile matches; only
the mounts start cold, once per pull request.

**The push says how long it took** (`Pushed REF (DIGEST) in 1.2s`), so a
slow push on a server shows in the build log instead of hiding between
the last BuildKit step and "Built".

## Consequences

The benchmark app, before (one core, gzip, tags) and after (two cores,
the default on a two-core server, zstd, pins), build and push:

| | Before | After |
|---|---|---|
| Dockerfile, cold | 59.8 s | 41.7 s |
| Dockerfile, warm (one-line change) | 35.4 s | 20.4 s |
| Dockerfile with the hinted cache mount, warm | 35.4 s without it | 15.8 s |
| Railpack, cold | 106.5 s | 65.3 s |
| Railpack, warm | 35.3 s | 15.9 s |

- What's left in a warm Dockerfile build is mostly the user's: here a
  `chmod -R` step that copies the app into a new layer (5.5 s), and
  TypeScript.
- Memory doesn't grow with the cap: Next.js sizes its worker pools by
  the host's cores, whatever the cgroup allows. `memory_mb` still caps
  a build, which fails saying so when it hits it.
- `TestBuildTimes` re-measures the build paths (pack, build, export,
  push, start; cold, then warm after a one-line change):
  `DOCK_BUILD_APP=$PWD/e2e/apps/nextjs-pnpm DOCK_BUILD_CPUS=2 go test
  -tags integration -run TestBuildTimes -v ./internal/node/build/`.
