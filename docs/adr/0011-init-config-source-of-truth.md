# 11. Interactive init and config.yaml as source of truth

- Status: Accepted
- Date: 2026-06-04
- Supersedes the input model of: [ADR-0004](0004-manage-sessions-require-existing-bastion.md) (bastion now discovered, not flag-supplied) and the slice-6 store

## Context

The shipped MVP derives cluster facts from the operator's *current kubeconfig
context* and resolves the bastion from a `--bastion-id` flag remembered in a
`bastions.json` store (cluster-OCID → bastion-OCID). That assumes the operator
already has the right kubeconfig context selected and knows their bastion OCID —
neither is true for a first-time operator who only knows "I have an OKE cluster
somewhere in this tenancy".

With the daemon model (ADR-0009), `up` runs unattended and needs an unambiguous,
pre-resolved source of truth rather than whatever context happens to be current.

## Decision

Add an interactive `init` command that produces a single `config.yaml` as the
source of truth, replacing the derive-from-kubeconfig + flag + store input model:

1. List the sections of `~/.oci/config` and have the operator pick a **profile**;
   record it (auth method auto-detected from the section — api_key vs
   security_token).
2. Walk all accessible compartments in the tenancy, aggregate ACTIVE OKE
   clusters into one list, and have the operator pick a **cluster**. Cluster
   facts (private endpoint, region) come from the OCI cluster object.
3. List **bastions** with access to that cluster and have the operator pick one
   (auto-select if exactly one) — filling the requirement of ADR-0004 (a bastion
   must pre-exist) through discovery instead of a hand-supplied OCID.
4. Generate the cluster's kubeconfig (OKE CreateKubeconfig) and merge the base
   context into `~/.kube/config`.
5. Write the choices to `config.yaml` (`clusters[]`, each carrying cluster,
   region, compartment, bastion, kube context). This supersedes `bastions.json`.

`init` writes config only; it does not start the daemon (ADR-0009).

## Consequences

- A first-time operator goes from nothing to a working, configured tunnel target
  without knowing any OCIDs or pre-arranging a kubeconfig context.
- `up` reads `config.yaml` deterministically — no dependency on which kubeconfig
  context is current, which matters for an unattended daemon.
- `init` needs broader IAM than the MVP: list compartments, list/inspect
  clusters, create-kubeconfig, list bastions (read-only discovery) in addition to
  the session permissions `up` already needs.
- Compartment-walking can be slow in large tenancies (many `ListClusters` calls);
  mitigated by concurrency and a progress indicator, not by caching (a one-time
  step).
- The bastion is still pre-existing and still required (ADR-0004 holds); only the
  *acquisition* changes from flag to discovery.
- `bastions.json` is retired; existing users' mappings are not auto-migrated (the
  bastion is re-selected during `init`).

## Alternatives considered

- **Keep deriving from the current kubeconfig context** — smaller, but breaks the
  unattended daemon (depends on ambient context) and keeps onboarding manual.
  Rejected.
- **init handles profile + cluster only, bastion stays a flag** — leaves
  onboarding half-done; the tunnel still can't open without a separately sourced
  bastion OCID. Rejected.
