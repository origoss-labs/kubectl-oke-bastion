# ai-docs

Curated reference material for building `kubectl-oke-bastion`. Distilled from the
official sources below so an agent (or human) has the relevant facts in-repo
without re-fetching. Each file names its source URL and the fetch date
(2026-06-03). Treat API signatures as a guide — verify exact field names against
the pinned SDK version when writing code.

| File | Source |
| --- | --- |
| [effective-go.md](effective-go.md) | https://go.dev/doc/effective_go |
| [oci-bastion-port-forwarding.md](oci-bastion-port-forwarding.md) | OCI Bastion + OKE access docs |
| [oci-go-sdk-bastion.md](oci-go-sdk-bastion.md) | https://pkg.go.dev/github.com/oracle/oci-go-sdk/v65/bastion |

See [`../docs/adr/`](../docs/adr/README.md) for how these inform our decisions —
notably ADR-0005 (we use a separate kubeconfig context + `tls-server-name`, not
the in-place `server:` edit the OCI docs show).
