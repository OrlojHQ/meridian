# ADR 0031: Report a structured Thread's model and let the operator switch it

- Status: Accepted
- Date: 2026-11-08
- Extends: ADRs 0013, 0026, 0028, and 0030
- Amends: ADR 0028 (model switching is no longer excluded)
- Amended by: ADR 0033 (reasoning effort travels with the model list), and
  the amendment below (subagent models in the Agents list)

## Context

The Thread composer does not say which model a structured session is using,
and the operator cannot change it. ADR 0028 left switching out because a
session's model comes from the Project's provider connection and harness setup
(ADR 0024), and a per-Thread switch could bypass that.

Every pinned harness reports its own model list and switches headlessly. These
were checked against the official pack images:

| Harness | Model list | Current model | Switch |
| --- | --- | --- | --- |
| Claude Code | `initialize` response `models[]` (`value`, `displayName`, `description`) | the selected `value`, `default` until changed | control request `set_model` |
| Codex | `model/list` `data[]` (`id`, `displayName`, `description`, `hidden`) | `thread/start` and `thread/resume` `model` | `model` on `turn/start`, kept for later turns |
| Pi | `get_available_models` `models[]` (`provider`, `id`, `name`) | `get_state` `model` | `set_model` with `provider` and `modelId` |
| ACP (OpenCode, `dsh`) | the `select` config option whose category is `model`, from `session/new`, `session/resume`, `session/load`, and `config_option_update` | its `currentValue` | `session/set_config_option` |

Each list comes from the configuration Meridian gave the harness. Pi lists only
models whose provider has credentials. The `dsh` route that `capsuled` writes
for a connection names exactly one model. OpenCode lists the providers its
configuration enables. Claude Code and Codex list their vendor's models.

The provider gateway does not restrict which model a request names, and Claude
Code's own `/model` command already switches models inside a Thread
(ADR 0028).

## Decision

### Protocol

- `meridian.adapter.v1` gains a `models` capability, an adapter-to-controller
  `models` frame, and a controller-to-adapter `set_model` frame. Both peers
  must set the capability. `capsuled` rejects a `models` frame from an adapter
  that did not negotiate it, and refuses to send `set_model` to one with
  `409 models_unsupported`.
- The `models` frame carries `current` and the complete `options` list. A later
  frame replaces the earlier one. Each option has an `id`, a `label`, and an
  optional `description`. `current` may be empty when the harness does not say,
  and it need not be one of the options.
- `set_model` carries an `id` and a `model`. The adapter switches only to an
  `id` from the list it last reported. Anything else, or a harness that refuses
  the switch, becomes a non-fatal `error` frame with code `model_unavailable`.
  The session continues on its previous model.
- After a successful switch the adapter reports the list again with the new
  `current`. The new model applies from the next turn. A turn that is already
  running keeps its model.

### Bounds

The list is hostile input. `adapterproto` rejects a frame with more than 128
options, an empty or duplicate `id`, an `id` or `current` over 256 bytes, a
`label` over 128 bytes, a `description` over 512 bytes, or any C0 or C1 control
character. Adapters fold whitespace, truncate on a rune boundary, drop entries
that would fail, and keep the current model when they truncate a long list.
Option IDs are opaque strings that only the reporting adapter interprets.

### Per harness

- `claude-stream` reports the `initialize` models, with `current` set to
  `default` until a `set_model` control request succeeds.
- `codex-app-server` calls `model/list`, skips hidden models, and takes
  `current` from `thread/start` or `thread/resume`. When the catalog lacks
  that opening model, as it does for a provider connection's default model,
  the adapter lists it first so a switch away can be undone. A switch is stored and sent
  as `model` on the next `turn/start`, which Codex keeps for later turns.
- `pi-rpc` calls `get_available_models` and `get_state`. Option IDs are
  `provider/id`. A switch is `set_model` with that provider and model ID.
- `acp` reads the first `select` config option whose category is `model`,
  flattening grouped options. A switch is `session/set_config_option` with that
  option's ID and the chosen value. The adapter reports the list again from the
  response or from `config_option_update`.

### Daemon, API, and UI

- `POST /threads/{threadId}/model` takes `model` and an expected resource
  version. The daemon checks that the current session negotiated `models`
  (otherwise `unsupported`), then appends the request to the encrypted
  transcript and delivers it as a `set_model` frame through the same durable,
  idempotent path as permission and input responses.
- `models` frames are persisted as encrypted JSON status blocks, like
  `commands` frames, so model names never appear in plaintext lifecycle
  records. `ThreadAdapterEvent` gains `type: models` and `type: set_model`.
- The composer shows the current model inside the message box next to the send
  button and opens a menu of the reported options. It renders every label as
  text. Threads whose session reports nothing show no picker.

## Consequences

The operator can see and change the model of Claude Code, Codex, Pi, OpenCode,
and DeepSeek Harness Threads without leaving the composer.

A switch does not widen what a session can reach. The endpoint and credential
still come from the Project's connection or the harness setup. The choice is
limited to models that configuration already lets the harness use, and the
gateway already forwards any model name the harness sends. A switch can raise
cost, because a pricier model may be on the list. It runs only from an
operator mutation, never from a Capsule, and the transcript records it.

A coordinated subagent starts on its harness's default model. A lead cannot
choose its subagents' models through `meridian-coord`.

A daemon older than this change does not know the `models` frame kind. Official
pack images share the daemon's release tag (ADR 0021), so they stay in step. A
custom adapter that does not negotiate `models` shows no picker.

Reasoning effort, which Claude Code, Codex, and `dsh` also report, is not part
of this decision. (ADR 0033 later adds it to the model list.)

## Amendment: subagent models in the Agents list

A lead's Agents list showed each subagent's name, harness, and phase, but not
which model it runs, so the operator had to open each subagent to see it.

The model a session runs is in its encrypted transcript. A link row stays
content-free in SQLite, so the label is derived like a link's phase: in
memory, never persisted.

- When the daemon ingests a `models` frame, it remembers the labels of the
  current model and effort for that Thread, after the transcript write
  commits.
- After a restart, before the session reports again, the labels come from
  the latest `models` block in the trailing transcript window that the
  attention derivation already decrypts. A report older than that window is
  not found until the session reports again.
- `ThreadLink` gains optional `model` and `effort` labels, filled only for a
  live child session. Labels are one line of at most 128 bytes and are
  rendered as text.

The labels go only to the authenticated operator API. The lead's
`meridian-coord` link view is unchanged, so a lead still cannot see or choose
its subagents' models.

## Amendment: lead-chosen subagent models and harnesses, and per-lead Worker Model defaults

Leads coordinate tasks of varying difficulty and benefit from choosing a faster
or stronger model, or another harness allowed by the Coordination Grant, without
requiring manual operator intervention on each spawn. Operators also need a
persisted per-lead default so a lead can run on one model while spawning workers
on another.

- `CoordinationGrant` stores a persisted `workerModel` setting (`mode`: `match`
  or `custom`, with optional `harness` and required `model` when `custom`).
  `match` (the default) uses the lead's current harness and model; `custom`
  uses the operator-chosen model (and optional harness, which must be in
  `allowedHarnesses`). Values are bounded, grammar-checked identifiers stored
  alongside `allowed_harnesses` in SQLite, never prompt content.
- `meridian.coord.v1` `spawn` makes `harness` optional and adds optional
  `model`. Resolution order is: explicit spawn override, then the grant's
  `workerModel` setting, then `match`. Cross-harness spawns that omit `model`
  use the target harness's default model rather than copying another harness's
  model ID.
- `allowedHarnesses` must always include the default spawn target: the
  `custom` setting's harness, or else the lead's own harness, which `match` and
  a harness-less `custom` setting spawn on. New leads allow only their own
  harness unless the operator adds others in Subagent settings.
- Invalid harnesses (`harness_not_allowed`) fail synchronously. A model a live
  session of the target harness does not offer (`model_unavailable`) also fails
  synchronously with the valid choices; a catalog from an ended session is
  advisory only, since the harness may have changed since. When a subagent has
  a requested model, `meridiand` holds its first prompt until the child session
  has finished its adapter handshake, reports its `models` frame, and switches
  to the requested model with the model's default effort. The harness has
  `CoordinationModelWait` (30 seconds) after the handshake to report and switch;
  a runtime or store error that persists for `CoordinationModelStall`
  (5 minutes) also fails the link. A model the lead or operator named (an
  explicit spawn `model` or a `custom` Worker Model) is strict: the link fails
  (`model_unavailable`) without sending the prompt if the harness does not
  support models, does not offer the model, refuses it, or does not switch in
  time. A model `match` chose is best effort, since nobody asked for it: in
  those cases the subagent starts on its harness's default model instead, and
  the link records `modelState` `default` (migration 0042) so the lead and UI
  report the model it actually runs, not the lead's. A persistent runtime or
  store error still fails either kind.
- `coordproto.Self` (`meridian_whoami`), the MCP `meridian_spawn` description,
  and the lead's start/resume coordination brief (`LeadWorkerBrief` in
  `StartMetadata`) share the allowed harnesses, known models per harness, and
  current worker default; `coordproto.Link.model` (always a model ID the lead
  can pass back to `spawn`) and `ThreadLink.requestedModel` expose each
  subagent's model to the lead and operator UI.
- The lead composer's model menu gains a **Worker Model** row (`Match` /
  `Custom`, with harness switcher when multiple harnesses are allowed) and a
  read-only hint line naming what new workers will use.
