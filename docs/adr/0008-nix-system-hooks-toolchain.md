# 8. Provide the dev/CI toolchain from a Nix flake; pre-commit hooks run as system hooks

- Status: Accepted
- Date: 2026-06-03

## Context

The project needs a reproducible developer and CI toolchain: `go`,
`golangci-lint`, `gofumpt`, `goreleaser`, `pre-commit`, `yamllint`, `krew`. Two
forces pull against each other.

A Go contributor expects the idiomatic GitHub Actions path: `setup-go`,
`golangci/golangci-lint-action`, and pre-commit hooks that fetch their own
pinned binaries from upstream hook repos. It is familiar and needs no extra
runtime.

But that path runs *two* toolchains — one resolved by Actions/pre-commit in CI,
another (whatever the developer happens to have on `PATH`) locally — and they
drift. A lint error or formatter diff that only one side sees is the exact class
of friction this repo wants to avoid, and `go 1.26` plus a current
`golangci-lint v2` are not reliably present on a contributor's machine.

## Decision

A single Nix flake (`.flakes/flake.nix`, tracked into the shell by direnv via
`.envrc` → `use flake ./.flakes`) is the **only** source of every tool. All
pre-commit hooks are declared `repo: local` with `language: system`: they invoke
the binary already on `PATH`, which under the flake is the Nix-provided one.

CI does not use `setup-go` or `golangci-lint-action`. Every job installs Nix and
runs the work inside the dev shell:
`nix develop ./.flakes -c <pre-commit | go test | goreleaser …>`. The command a
developer runs locally and the command CI runs are byte-for-byte the same
binary.

File-hygiene checks that would otherwise come from the upstream
`pre-commit/pre-commit-hooks` repo (trailing whitespace, final newline) are
instead covered by `gofumpt` and `golangci-lint`'s `whitespace` linter for Go,
and `yamllint` for YAML — all Nix-provided — so no hook reaches outside the
flake for its toolchain.

## Consequences

- One toolchain. `flake.lock` pins it; bumping it is a deliberate, reviewable
  change that moves local and CI in lockstep.
- A contributor must have Nix (and ideally direnv) installed. That is the cost
  of entry; the README documents it.
- `go 1.26`/`golangci-lint v2` availability is guaranteed by the flake, not by
  whatever the runner image or developer ships.
- CI loses the per-action conveniences (annotations from
  `golangci-lint-action`, automatic Go caching). `cache-nix-action` covers the
  store; lint output is plain text in the job log.
- pre-commit still clones nothing for our hooks — `language: system` means no
  per-hook virtualenv, so hook setup is instant and offline.

## Alternatives considered

- **`setup-go` + `golangci-lint-action` + upstream pre-commit hooks** — the
  idiomatic Go-on-Actions setup. Familiar and zero Nix dependency, but runs a
  different toolchain in CI than on the developer's machine, which is the drift
  this decision exists to kill. Rejected.
- **Hybrid: generic hooks from `pre-commit/pre-commit-hooks`, Go tools from
  Nix** — fewer reinvented checks, but reintroduces a second
  (pre-commit-managed) toolchain for the generic hooks and a network fetch on
  hook install. Rejected in favour of covering hygiene with Nix-provided
  linters.
- **Nix without direnv (`nix develop` by hand)** — works, but an un-activated
  shell silently falls back to whatever `go` is on `PATH`, reopening the drift.
  direnv makes activation automatic; `nix develop` remains the manual fallback.
