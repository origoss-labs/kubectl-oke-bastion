# Architecture Decision Records

Foundational decisions for `kubectl-oke-bastion`. Each records a fork that is
hard to reverse, surprising without context, or the result of a real trade-off.

| ADR | Decision |
| --- | --- |
| [0001](0001-go-binary-with-oci-sdk.md) | Implement as a Go binary using the OCI SDK |
| [0002](0002-embedded-crypto-ssh.md) | Drive the local forward with embedded crypto/ssh |
| [0003](0003-foreground-supervisor.md) | Run the supervisor in the foreground, not as a daemon _(superseded by 0009)_ |
| [0004](0004-manage-sessions-require-existing-bastion.md) | Manage sessions only; require a pre-existing Bastion |
| [0005](0005-non-destructive-kubeconfig-bastion-context.md) | Wire the tunnel via a separate kubeconfig context with tls-server-name |
| [0006](0006-reactive-recreate-proactive-deferred.md) | Reactive recreate for v1; proactive rotation deferred _(superseded by 0010)_ |
| [0008](0008-nix-system-hooks-toolchain.md) | Provide the dev/CI toolchain from a Nix flake; pre-commit hooks run as system hooks |
| [0009](0009-background-daemon-supersedes-foreground.md) | Background daemon model _(supersedes 0003)_ |
| [0010](0010-time-aware-unified-rebuild.md) | Time-aware unified rebuild _(supersedes 0006)_ |
| [0011](0011-init-config-source-of-truth.md) | Interactive init + config.yaml as source of truth |
