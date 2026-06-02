# 1. Implement as a Go binary using the OCI SDK

- Status: Accepted
- Date: 2026-06-03

## Context

Every existing tool that tunnels into a private OKE cluster via OCI Bastion is a
bash script wrapping the `oci` CLI plus `jq` and the system `ssh` client
(`sibil/oci_bastion`, `oracle-devrel/oke-bastion`, the A-Team and misaac.me
guides). None of them survive a dropped connection or the 3-hour session expiry —
they are one-shot. Our differentiator is exactly that resilience: a process that
holds the tunnel open across breaks and expiries.

We need a runtime that gives us robust process control (signals, timers, a clean
supervision loop), a static single binary suitable for distribution (krew), and
direct API access without bolting external tools onto the user's machine.

## Decision

Build the plugin as a compiled **Go** binary that talks to OCI through the
**OCI Go SDK** directly. Do not depend on the `oci` CLI, `jq`, or a Python
runtime at runtime.

## Consequences

- One static binary on `PATH` is all kubectl needs — no `oci`/`jq`/`python`
  install or `~/.oci` CLI version coupling.
- Bastion session create/get/delete and `GetCluster` calls go through typed SDK
  clients; no shelling out and no parsing CLI text.
- More upfront code than a shell script, and we own OCI auth resolution
  ourselves (see ADR-0007 if/when auth grows).
- Go's standard library covers the supervision loop, signals, and timers the
  resilience goal depends on.

## Alternatives considered

- **Bash + `oci` CLI** — fastest to write and matches all prior art, but
  fragile lifecycle (background loops, PID tracking, stderr parsing) is precisely
  why no existing script is resilient. Rejected.
- **Go shelling out to `oci` CLI** — keeps SDK code small but reintroduces the
  CLI as a runtime dependency and version-coupling. Rejected.
- **Python + OCI SDK** — needs a Python runtime/venv on every operator machine;
  awkward for a drop-in kubectl binary. Rejected.
