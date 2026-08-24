# Agent guidance

## Scope

Meridian is coding-specific infrastructure, not a general orchestration engine. Preserve the boundaries and security invariants in `docs/adr` and `docs/threat-model.md`.

## Toolchain

- Go module: `github.com/OrlojHQ/meridian`
- Go version: 1.26.5
- Frontend: Bun, strict TypeScript, React, and Vite
- API contract: OpenAPI 3.1 in `api/openapi.yaml`

Use `make bootstrap` before development. Run `make build`, `make test`, `make lint`, `make ui-build`, and `git diff --check` before handing off changes.

## Generated code

`pkg/client` and `frontend/src/api/generated` are generated from `api/openapi.yaml`. Never edit them manually. Run `make generate`, then `make check-generated`.

Add dependencies through `go get`, `go get -tool`, or `bun add`; do not invent versions or use floating versions in CI. Do not mutate global developer tooling.

## Security

Treat repositories, dependencies, agent output, terminal data, and Capsule processes as hostile. Never persist credential plaintext or expose a host Docker socket/runtime API to a Capsule. Do not claim container isolation supports untrusted multi-tenancy. Security-sensitive architecture changes require an ADR and a threat-model update.

## Change discipline

Keep changes within Meridian's product boundary. Add no placeholder behavior
that could be mistaken for an implemented service. Keep build/version metadata
shared across binaries and preserve useful help/version behavior.
