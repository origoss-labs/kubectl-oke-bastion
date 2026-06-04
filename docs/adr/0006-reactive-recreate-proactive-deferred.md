# 6. Reactive recreate for v1; proactive rotation deferred

- Status: Superseded by [ADR-0010](0010-time-aware-unified-rebuild.md)
- Date: 2026-06-03

## Context

The supervisor must recover from two failure modes:

- **Break** — SSH connection drops while the session is still valid. Cheap:
  redial the forward, no new session.
- **Expiry** — the session reaches its hard 3-hour OCI TTL and ends. Requires
  creating a new session, then a new tunnel.

Expiry timing is *known at session creation*, so we could rotate proactively —
build the replacement at `T-margin`, swap the local listener, tear down the old
session — for near-zero downtime that wouldn't interrupt a long-running
`kubectl logs -f` / `watch` / `port-forward`. That needs overlapping sessions and
a listener-swap path: more loop logic and more to test.

## Decision

For v1, recover **reactively** for both modes: a health probe detects a dead
tunnel (dropped, deleted, or expired session) and rebuilds it — a single code
path. Sessions are created with the maximum TTL to minimise rebuild frequency.

Proactive make-before-break rotation across the TTL boundary is **deferred** and
planned as the next enhancement once the reactive core is proven.

## Consequences

- Simplest possible supervisor: one "is it alive? if not, rebuild" loop covers
  break and expiry alike.
- A visible outage occurs at each ~3-hour boundary and on each transient blip;
  in-flight long-running kubectl commands can break across a rebuild. Acceptable
  for v1, and the motivation for the deferred work.
- The proactive design is recorded here so its rationale survives to the
  follow-up implementation; it must not regress the reactive path it builds on.

## Alternatives considered

- **Proactive rotation from day one** — near-zero downtime, but overlapping
  sessions plus listener-swap is more logic and test surface than a first cut
  warrants. Deferred, not rejected.
