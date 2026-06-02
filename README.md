# kubectl-oke-bastion

A kubectl plugin that opens and **supervises** an OCI Bastion SSH port-forward
tunnel to a private OKE cluster's Kubernetes API endpoint. Unlike the one-shot
scripts this replaces, it reconnects when the tunnel breaks and rebuilds the
session when it hits OCI's hard 3-hour TTL — so `kubectl` keeps working without
you re-running anything.

> Status: early. The repo currently holds the design (glossary + ADRs); the
> implementation follows.

## How it works

```
kubectl ──▶ 127.0.0.1:<port> ──[SSH local forward]──▶ OCI Bastion ──▶ OKE private endpoint :6443
                                         ▲
                                  supervisor loop: redial on break, recreate session on expiry
```

`kubectl oke bastion` runs in the foreground (like `kubectl port-forward`),
holds the tunnel, and tears everything down on Ctrl-C.

## Prerequisites

- A **pre-existing OCI Bastion** with access to the cluster's subnet. The plugin
  manages sessions on it but does not create the Bastion itself (provision it in
  Terraform — see `tofu-oci-foundation`).
- An OKE kubeconfig for the target cluster as your current context.
- OCI credentials (see Auth below).

## Usage (planned surface)

```bash
# First run: point it at your Bastion (remembered per-cluster afterwards)
kubectl oke bastion --bastion-id ocid1.bastion.oc1..xxxx

# Subsequent runs: cluster + bastion derived automatically
kubectl oke bastion

# Then, in another shell:
kubectl --context <cluster>-bastion get pods -A
```

Cluster private endpoint, cluster OCID, and region are read from your current
kubeconfig context. Only the Bastion OCID is supplied externally, and it is
remembered (keyed by cluster OCID) so later runs need no flags.

## Design decisions

The decisions that shape the tool — and why — live in
[`docs/adr/`](docs/adr/README.md). The terms used throughout are defined in
[`CONTEXT.md`](CONTEXT.md). In brief:

- **Go + OCI SDK**, single static binary, no `oci`/`jq`/`ssh` runtime deps
  ([ADR-0001](docs/adr/0001-go-binary-with-oci-sdk.md)).
- **Embedded `crypto/ssh`** for the forward, for deterministic reconnect
  ([ADR-0002](docs/adr/0002-embedded-crypto-ssh.md)).
- **Foreground supervisor**, not a daemon
  ([ADR-0003](docs/adr/0003-foreground-supervisor.md)).
- **Manage sessions only**; require an existing Bastion
  ([ADR-0004](docs/adr/0004-manage-sessions-require-existing-bastion.md)).
- **Non-destructive kubeconfig** via a separate `-bastion` context using
  `tls-server-name` so TLS still verifies
  ([ADR-0005](docs/adr/0005-non-destructive-kubeconfig-bastion-context.md)).
- **Reactive recreate** in v1; proactive rotation deferred
  ([ADR-0006](docs/adr/0006-reactive-recreate-proactive-deferred.md)).

Settled defaults (not ADR-worthy, easily reversible):

- **Auth** — defaults to `~/.oci/config` with `--profile`; `--auth` selects
  `api_key` | `security_token` (browser `oci session authenticate`) |
  `instance_principal`.
- **Local port** — OS-assigned ephemeral by default; pin with `--local-port`.
- **Teardown** — on clean exit the active session is deleted, the ephemeral key
  dropped, and the `-bastion` kubeconfig entry removed. Only a hard crash leaves
  them behind, and the next run overwrites the entry.

## Roadmap

- Proactive make-before-break session rotation across the 3h TTL boundary
  ([ADR-0006](docs/adr/0006-reactive-recreate-proactive-deferred.md)).
- krew distribution manifest.

## License

Apache-2.0. See [LICENSE](LICENSE).
