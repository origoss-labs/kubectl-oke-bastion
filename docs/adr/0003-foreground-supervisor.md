# 3. Run the supervisor in the foreground, not as a daemon

- Status: Accepted
- Date: 2026-06-03

## Context

The tunnel needs a long-running owner — the supervisor — that watches for breaks
and expiries and rebuilds. That owner can either block the terminal (like
`kubectl port-forward`) or detach as a background daemon managed by
`start`/`stop`/`status` subcommands.

A daemon survives terminal close and lets one tunnel serve many shells, but costs
real complexity: daemonization, PID files, log files, a status/IPC surface, and
the orphaned-process failure modes that come with all of it.

## Decision

`kubectl oke bastion` runs the supervisor in the **foreground** and blocks,
holding the tunnel for the life of the process. Ctrl-C / SIGTERM tears everything
down. No background daemon, no PID/log/state files for v1.

## Consequences

- Matches the `kubectl port-forward` mental model operators already have.
- Lifecycle is bounded by the process: start = up, exit = fully torn down (see
  ADR-0006 for in-life rebuilds, and the teardown decision in the README).
- The terminal is occupied for the session's duration, and the tunnel dies when
  the terminal closes — acceptable for an interactive operator tool.
- Far less code and fewer failure modes than a daemon; no orphan/stale-PID class
  of bugs.

## Alternatives considered

- **Background daemon + subcommands** — survives terminal close and persists
  across shells, but the daemonization/PID/log/IPC surface is a large jump in
  complexity not justified for v1. Reconsider if multi-shell persistence becomes
  a real need.
- **Foreground default with `--detach` opt-in** — best of both, but doubles the
  lifecycle code and test surface from day one. Deferred.
