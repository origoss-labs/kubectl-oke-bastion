# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

This repo is **single-context**: one `CONTEXT.md` + `docs/adr/` at the repo root.

## Before exploring, read these

- **`CONTEXT.md`** at the repo root — the glossary of domain language.
- **`docs/adr/`** — read ADRs that touch the area you're about to work in. Note that some ADRs supersede earlier ones (each carries a `Status:` / `Superseded by` header); follow the live decision, not the superseded one.

If any of these files don't exist, **proceed silently**. Don't flag their absence; don't suggest creating them upfront. The producer skill (`/grill-with-docs`) creates them lazily when terms or decisions actually get resolved.

## File structure

```
/
├── CONTEXT.md
├── docs/adr/
│   ├── 0001-go-binary-with-oci-sdk.md
│   ├── ...
│   └── 0011-init-config-source-of-truth.md
└── internal/
```

(If this ever becomes a monorepo, a `CONTEXT-MAP.md` at the root would point to per-context `CONTEXT.md` files under each `src/<context>/`, each with its own `docs/adr/`. Not the current layout.)

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in `CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal — either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/grill-with-docs`).

## Flag ADR conflicts

If your output contradicts an existing (non-superseded) ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-0005 (non-destructive kubeconfig) — but worth reopening because…_
