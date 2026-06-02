# 5. Wire the tunnel via a separate kubeconfig context with tls-server-name

- Status: Accepted
- Date: 2026-06-03

## Context

For kubectl to use the tunnel, kubeconfig must point at the local end
(`https://127.0.0.1:<local-port>`). Two problems follow:

1. **TLS.** The OKE API server certificate is issued for the cluster's private
   endpoint IP, not `127.0.0.1`. Pointing `server:` at localhost naively fails
   certificate verification.
2. **Blast radius.** Mutating the operator's existing cluster entry risks leaving
   a broken pointer to a dead local port if the process crashes mid-session.

kubectl supports `tls-server-name` on a cluster entry: it dials the `server:`
address but presents that name for SNI and validates the cert against it.

## Decision

Add a **separate** kubeconfig cluster+context entry (suffix `-bastion`):

- `server: https://127.0.0.1:<local-port>`
- `tls-server-name:` set to the original private endpoint the cert is issued for
- reuse the existing cluster CA data

The operator's original context is never modified. The `-bastion` entry is
removed on clean exit (see the teardown decision in the README).

## Consequences

- TLS verification succeeds against the real cluster CA — no
  `insecure-skip-tls-verify`, no security downgrade.
- Non-destructive and crash-safe: a hard crash leaves at worst a stale, clearly
  named `-bastion` entry that the next run overwrites; the working context is
  untouched.
- Operator selects the tunnel with `kubectl --context <name>-bastion ...` (the
  plugin may also set it as current-context for the session).

## Alternatives considered

- **Rewrite the existing entry in place, restore on exit** — transparent to
  current kubectl usage, but mutates the working file and a crash leaves it
  pointing at a dead port. Rejected.
- **Don't touch kubeconfig, print flags for manual wiring** — zero magic, but
  defeats the "automatic" goal of the tool. Rejected.
- **`insecure-skip-tls-verify`** — sidesteps the SNI issue by abandoning cert
  verification entirely. Rejected on security grounds.
