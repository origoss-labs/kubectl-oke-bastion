# Architecture Decision Records

Foundational decisions for `kubectl-oke-bastion`. Each records a fork that is
hard to reverse, surprising without context, or the result of a real trade-off.

| ADR | Decision |
| --- | --- |
| [0001](0001-go-binary-with-oci-sdk.md) | Implement as a Go binary using the OCI SDK |
| [0002](0002-embedded-crypto-ssh.md) | Drive the local forward with embedded crypto/ssh |
| [0003](0003-foreground-supervisor.md) | Run the supervisor in the foreground, not as a daemon |
| [0004](0004-manage-sessions-require-existing-bastion.md) | Manage sessions only; require a pre-existing Bastion |
| [0005](0005-non-destructive-kubeconfig-bastion-context.md) | Wire the tunnel via a separate kubeconfig context with tls-server-name |
| [0006](0006-reactive-recreate-proactive-deferred.md) | Reactive recreate for v1; proactive rotation deferred |
| [0008](0008-nix-system-hooks-toolchain.md) | Provide the dev/CI toolchain from a Nix flake; pre-commit hooks run as system hooks |
