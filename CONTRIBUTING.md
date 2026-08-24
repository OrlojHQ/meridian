# Contributing to Meridian

Meridian is at repository-foundation stage. Discuss large product or architecture changes before implementation, and record durable decisions in an ADR.

## Prerequisites

- Go 1.26.5
- Bun 1.3.0
- Git and Make

Install pinned dependencies:

```console
make bootstrap
```

## Development checks

Run the same core checks used by CI:

```console
make build
make test
make lint
make ui-build
git diff --check
```

When `api/openapi.yaml` changes, regenerate both clients and include them in the same change:

```console
make generate
make check-generated
```

Do not edit files under `pkg/client` or `frontend/src/api/generated` by hand.

## Changes

Keep changes focused, add tests for behavior, and update documentation when contracts or security assumptions change. Use `gofmt` for Go and retain strict TypeScript checks. Never commit credentials, local databases, generated build output, or dependency directories.

By contributing, you agree that your contribution is licensed under Apache License 2.0.
