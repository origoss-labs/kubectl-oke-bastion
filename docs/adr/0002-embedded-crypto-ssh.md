# 2. Drive the local forward with embedded crypto/ssh

- Status: Accepted
- Date: 2026-06-03

## Context

An OCI Bastion port-forwarding session is *consumed over SSH*: the operator runs
something like `ssh -N -L <local>:<private-ip>:6443 <session-ocid>@host.bastion.<region>.oci.oraclecloud.com`.
So even with the SDK creating sessions (ADR-0001), something still has to run the
SSH local forward.

The plugin's whole value is detecting a dropped tunnel and reconnecting cleanly.
Every prior-art tool delegates the forward to the system `ssh` binary, where
failure detection means watching a child process exit or scraping its stderr —
the brittle path that left all of them non-resilient.

## Decision

Open the SSH local port-forward **in-process** with `golang.org/x/crypto/ssh`.
Do not spawn the system `ssh` binary.

## Consequences

- Zero external runtime dependency on an `ssh` client; behaviour is identical
  across operator machines.
- Connection failures surface as Go errors on the forwarded conn / `ssh.Client`,
  so the supervisor reacts immediately and deterministically instead of parsing
  text or polling a PID.
- We own host-key handling and ephemeral keypair generation rather than relying
  on the user's `~/.ssh` config and `known_hosts`.
- Slightly more code than `exec.Command("ssh", ...)`, and we must track
  upstream `x/crypto/ssh` for security fixes.

## Alternatives considered

- **Shell out to system `ssh`** — trivial and battle-tested forwarding, but adds
  an ssh-binary dependency and reduces failure handling to child-process/stderr
  watching. This is the design that produced "no auto-reconnect" everywhere in
  the prior art. Rejected.
