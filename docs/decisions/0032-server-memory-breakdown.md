# 0032: Where a server's memory goes

Status: accepted (decided by the agent, on Saile's direction)

## Context

A server's page showed 1.8 GB of 3.7 GB used, but its services added up
to about 200 MB. The rest was BuildKit, Docker, Caddy, dock, the kernel and
the OS, and none of it was shown, so an operator couldn't tell what to cut
on a small single-server install.

## Decision

Every server sample (ADR 0015) carries a memory breakdown: parts in four
groups, apps, platform, other system and kernel, that add up to the used
total (`MemTotal - MemAvailable`). The server page shows it as a bar and a
table whose groups open to list their parts.

- **What a part counts.** For processes, `anon + shmem` from their cgroup's
  `memory.stat`: what they hold that can't be reclaimed. Page cache is left
  out because `MemAvailable` counts it as available; counting it would
  explain memory that isn't "used" (BuildKit's build cache would look
  huge). An app's part is therefore smaller than its usage in the services
  table, which counts active page cache like `docker stats`.
- **Where parts come from.** The node walks the cgroup v2 tree one level
  below the root and `system.slice`. Known units are Dock's platform (dock,
  Caddy, cloudflared, Docker and containerd); containers are described once
  through Docker (an app by its service label, BuildKit and the registry by
  role or name, anything Dock didn't create as "other containers", without
  its name) and remembered while they exist. Everything else is other
  system, its ten biggest units by name.
- **Kernel.** Slab, page tables, kernel stacks and per-CPU memory from
  `/proc/meminfo`. What nothing explains is "reserves and other": mostly
  the free memory the kernel holds back for its watermarks, which
  `MemAvailable` subtracts.
- **Names.** The control plane names app parts after their services with
  the names its stats collector already loads, so the breakdown costs no
  queries.
- Only the latest sample has a breakdown; history stays one memory number
  per point.

## Consequences

- An operator can see that BuildKit, snapd or the kernel is what fills a
  small server, and act on it.
- Each sample reads one more small file per unit and container (about
  0.4 ms for 100 cgroups) and asks Docker about each new container once.
- Older nodes send no breakdown; the section stays hidden for them.
