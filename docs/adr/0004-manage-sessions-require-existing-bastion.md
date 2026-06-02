# 4. Manage sessions only; require a pre-existing Bastion

- Status: Accepted
- Date: 2026-06-03

## Context

The plugin could provision the OCI Bastion resource itself when none exists, or
require one to already exist and only manage the port-forwarding sessions on it.

A Bastion is heavyweight infrastructure: it needs a target subnet, a client CIDR
allowlist, create-bastion IAM permissions, and it has its own lifecycle and
teardown semantics (if we create it, do we delete it on exit?). Sessions, by
contrast, are cheap, ephemeral, and the thing that actually breaks and expires.

Origoss already provisions foundational OCI infrastructure declaratively in
`tofu-oci-foundation`, where a Bastion belongs.

## Decision

The plugin **requires a pre-existing Bastion** (its OCID supplied once, then
remembered) and manages only **port-forwarding sessions** on it. It never
creates or deletes the Bastion resource.

## Consequences

- Tight scope and a small IAM footprint: session CRUD plus `GetCluster`, no
  create-bastion / subnet / VCN permissions.
- Clean separation of concerns — Bastion provisioning stays in Terraform where
  it is reviewed and version-controlled.
- The operator must have a Bastion already (and supply its OCID the first time).
  This is documented as a prerequisite, not a failure the tool tries to fix.

## Alternatives considered

- **Create the Bastion if missing, then sessions** — more turnkey for users with
  no infrastructure, but pulls subnet/CIDR/IAM concerns and Bastion-teardown
  ambiguity into a tool that should be about tunnel lifecycle. Rejected.
