# ADR 0040: Harness-pack manifests for personal setups

- Status: Accepted
- Date: 2026-11-10

## Context

Personal harness setups (`docs/personal-harness-setups.md`) were described by
Go switches in `internal/harnesssetup` and `internal/capsuleproto`: which
laptop directory each harness uses, which files under it may be imported,
which of them are parsed and sanitized as configuration, which variable
points the harness at the Capsule home, the empty instructions file a
connection-only home gets, and which provider connection types the harness
accepts. Adding a harness meant editing six switches in three packages, and
the path allowlist, which is a security control, was spread across them.

Issue #83 asks for one workstation profile that covers every official
harness. That starts with describing each harness as data.

## Decision

Each official pack ships `images/capsule-<name>/manifest.yaml` next to its
`pack.env` and `project.yaml`. The manifest declares:

- `root`: the home-relative configuration directory, the variable that
  overrides it on the laptop and that `capsuled` sets in the Capsule, and
  whether its laptop default follows `XDG_CONFIG_HOME`;
- `paths`: the root-relative files and directories (`/` suffix) that may be
  imported;
- `configFiles` and `configNames`: which of those are parsed and sanitized,
  by exact path or by file name anywhere under the root;
- `homeFiles`: configuration files directly in the home, such as
  `.claude.json`;
- `shared`: home directories several harnesses read, such as
  `.agents/skills`;
- `instructions`: the file created empty when a connection needs a home and
  no setup was saved;
- `connection.providers`: the provider connection types the harness accepts,
  and whether each needs a default model.

The manifests are embedded with `go:embed` (package `images`) and compiled
into `meridian`, `meridiand`, and `capsuled`. The CLI importer, the browser
review, server-side bundle validation, and `capsuled` home materialization
read the same copy. Nothing is read from a pack image at run time, so a
Capsule image cannot change what is imported or accepted.

Manifests are parsed strictly. An unknown field, a path that is absolute,
unclean, or leaves the home, a path containing a denied name, a reserved or
duplicate root variable (`HOME`, `PATH`, `XDG_*`, `MERIDIAN_*`, `LD_*`,
`DYLD_*`, `NODE_*`, `SSL_*`, `GIT_*`), a configuration file without a
supported parser, or an unknown provider type rejects that manifest. Its
harness is then unsupported, which fails closed. Tests require every shipped
manifest to load, every pack to have one, and the API harness enum to match.

The deny list stays in Go and applies before any manifest is consulted:
`auth.json`, `.credentials*`, `.env*`, `*.pem`, `*.key`, `.git`, and
`node_modules` are never imported, at any depth. A manifest can narrow what
is imported, never widen that list. Sanitizing credential fields and trust
overrides is unchanged and does not depend on the manifest.

The connection overlay, which edits each harness's native configuration to
use the provider gateway, stays in Go for now. It is the open design question
in #83: a declared patch that does not grow into a templating language. Until
that is settled, a manifest names the provider types a harness accepts and
the overlay remains per-harness code.

## Consequences

This change does not alter what any existing harness imports or accepts. The
switches were compared with the manifests over every combination of the
known roots, directories, and file names.

`capsuled` now sets every manifest's root variable in a setup home, so
`DSH_HOME` is set for every harness, as `CODEX_HOME` and `CLAUDE_CONFIG_DIR`
already were. Each points into the private home.

Adding a harness to personal setups still needs the API enum entry in
`api/openapi.yaml` and, for provider connections, an overlay in
`internal/capsuleproto`. A harness without a manifest still runs; it just has
no personal import.
