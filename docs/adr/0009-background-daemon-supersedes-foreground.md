# 9. Background daemon (supersedes ADR-0003)

- Status: Accepted
- Date: 2026-06-04
- Supersedes: [ADR-0003](0003-foreground-supervisor.md)

## Context

ADR-0003 chose a foreground supervisor (`kubectl oke bastion` blocks like
`kubectl port-forward`) and explicitly rejected a background daemon to avoid
daemonization, PID/log/state files, and a status/IPC surface.

Operating the tool revealed that the foreground model fails the actual need: the
operator wants `kubectl` to *just work* against the private cluster without a
terminal pinned to a tunnel, and without thinking about the tunnel at all. A
3-hour-lived, self-rebuilding tunnel that occupies a terminal is the wrong shape
for that. "Hide the tunnel from the operator" is the requirement; a foreground
process cannot meet it.

## Decision

Run the supervisor as a **detached background daemon**, one per configured
cluster. `up` spawns it and returns immediately; `down` stops it; `status`
inspects it. The daemon persists across terminal sessions until stopped.

Per-cluster coordination is file-based, not an IPC service:

- a **PID file** so `status`/`down` find the process;
- a **state file** the daemon updates (phase, session deadline, local port,
  restart count, last error) for `status` to read;
- a **log file** capturing daemon output, since nothing is on a terminal.

`down` sends SIGTERM; the daemon then tears down (delete session, drop the
ephemeral key, unwire the `-bastion` context). A foreground mode is retained for
debugging behind `up --foreground`, reusing the same supervisor code.

## Consequences

- Meets the "hidden" requirement: no terminal pinned, tunnel survives shell exit.
- Reintroduces exactly the complexity ADR-0003 avoided — daemonization (re-exec
  self, setsid, stdio→logfile), PID/state/log file lifecycle, and the
  stale-PID / orphan-process failure class. Accepted as the cost of the UX.
- Errors are no longer visible on a terminal; they must surface via the log file
  and the last-error field `status` reports. Silent failure is the risk to guard
  against (see [ADR-0010](0010-time-aware-unified-rebuild.md) capped backoff).
- File-based control (not a unix socket) keeps the surface small; richer live
  queries would need an IPC protocol we deliberately skip.

## Alternatives considered

- **Keep foreground, detach via the shell (`&`/nohup) or an emitted
  launchd/systemd unit)** — no daemon code, but pushes process management onto
  the operator and per-OS unit files; not "hidden", just relocated. Rejected.
- **Hybrid foreground-default + `--detach`** — doubles the lifecycle surface
  from day one. We instead make detached the default and keep `--foreground`
  only as a debug affordance.
