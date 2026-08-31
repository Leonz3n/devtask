# Domain Docs

This repository uses a single-context domain documentation layout.

## Before exploring

- Read `CONTEXT.md` at the repository root.
- Read relevant ADRs under `docs/adr/`.
- If either location does not exist, proceed silently.

## Use the glossary vocabulary

Use domain terms exactly as defined in `CONTEXT.md` in issues, specifications, implementation plans, code, and tests. Avoid synonyms that the glossary explicitly rejects.

If a required concept is absent, reconsider whether new terminology is necessary or note the gap for domain modeling.

## Respect ADRs

Surface any conflict with an existing ADR explicitly rather than silently overriding it.

## Layout

- `CONTEXT.md` contains the domain glossary only.
- `docs/adr/` contains repository-wide architectural decisions.
