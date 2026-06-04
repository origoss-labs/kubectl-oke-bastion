# 10. Time-aware unified rebuild (supersedes ADR-0006)

- Status: Accepted
- Date: 2026-06-04
- Supersedes: [ADR-0006](0006-reactive-recreate-proactive-deferred.md)

## Context

ADR-0006 split recovery into two paths — cheap redial-on-break (session still
valid) and recreate-on-expiry — and deferred proactive rotation as future work.

Two things changed the calculus. First, the tunnel is now owned by a background
daemon (ADR-0009) that knows each session's creation time, so the 3-hour TTL
deadline is always known locally — the information proactive rotation needs is
already in hand. Second, operational experience is that the tunnel "usually
breaks" outright rather than suffering a recoverable SSH blip while the session
stays healthy, so the redial-only fast path rarely applies and isn't worth its
extra state.

## Decision

Collapse recovery into a single **rebuild** action — new session + new tunnel on
the same local port (the `-bastion` context is left in place) — triggered by
either condition:

- **Break** — detected immediately via the tunnel's error signal (event-driven,
  not polled).
- **Near-expiry** — a periodic deadline check (~30s tick) fires a *proactive*
  rebuild when the remaining TTL drops below a margin (~5 min before the 3-hour
  cap).

If a rebuild itself fails (bastion down, OCI API error), retry with exponential
backoff capped (~1s→30s), recording the failure as `last-error` in the daemon's
state file. This un-defers the proactive rotation ADR-0006 postponed and removes
its redial/recreate distinction.

## Consequences

- One code path for break and expiry alike — simplest possible loop, matching the
  "simply restart" instinct.
- Proactive rebuild before the TTL boundary avoids the guaranteed ~3h outage the
  old reactive path incurred; in-flight long-running `kubectl` is interrupted far
  less often.
- A transient SSH blip now costs a full session recreate (a few OCI API calls /
  seconds) rather than a sub-second redial. Accepted: such blips are rare in
  practice here, and the simpler single path is worth more than the saved
  milliseconds.
- Capped backoff plus `last-error` ensures a hidden daemon never hot-loops
  silently against a down bastion (ADR-0009 made silence the real risk).

## Alternatives considered

- **Keep ADR-0006's two paths and merely add proactive preempt** — faster
  transient recovery, but more states to implement and test, against the
  simplicity goal. Rejected.
- **Proactive-only rotation, no break detection** — simplest loop, but a mid-TTL
  break would go unnoticed until the next rotation, contradicting "it usually
  breaks". Rejected.
