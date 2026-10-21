# 0012: Name: Tempo Dock, CLI `dock`

Status: accepted

## Context

`platform` was a placeholder. The binary name, CLI commands, config file and
container labels get baked into docs, scripts and agent prompts, so the name
had to be settled before M1. NordTempo products are named "Tempo" plus a short
subname (Tempo Lift, Tempo Live).

## Decision

The product is **Tempo Dock**: the place your apps dock on servers you own.
It fits the Docker-native, registry-and-promotion model, and matches the
short, one-syllable siblings.

The subname alone is the technical name:

- Binary and CLI: `dock` (`dock deploy`, `dock serve`, `dock node`)
- Repo config file: `dock.toml`
- Container labels: `dock.project`, `dock.env`, `dock.service`, `dock.release`
- Entry point: `cmd/dock`

Rejected: Tempo Stage (clashes with the `staging` environment), Tempo Cue
(`cue` is the CUE language CLI), Tempo Port (`port` is MacPorts), Tempo
Harbor, Fleet and Keel (established infra tools).

## Consequences

- Short commands that read naturally in docs and agent prompts.
- `dock` is a common word, so search results and package names need the full
  "Tempo Dock" name.
- Grafana Tempo is a well-known observability project. Fine while internal;
  worth revisiting for the whole Tempo family if we open source
  ([0008](0008-keep-doors-open.md)).
