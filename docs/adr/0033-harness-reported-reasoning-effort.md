# ADR 0033: Report a structured Thread's reasoning effort and let the operator change it

- Status: Accepted
- Date: 2026-11-08
- Extends: ADRs 0013, 0030, and 0031
- Amends: ADR 0031 (reasoning effort is no longer excluded)

## Context

ADR 0031 lets the operator see and switch a structured session's model from
the composer, and left reasoning effort out. Coding agents expose effort as a
per-model setting next to the model, and the operator needs it to trade
latency and cost against depth on a given turn.

Every pinned harness reports and changes effort headlessly. These were checked
against the official pack images:

| Harness | Levels | Current | Change |
| --- | --- | --- | --- |
| Claude Code | `initialize` `models[]` `supportedEffortLevels` when `supportsEffort` | not reported; Claude Code's default until changed | control request `apply_flag_settings` with `{"effortLevel": level}` |
| Codex | `model/list` `supportedReasoningEfforts[]` (`reasoningEffort`, `description`) and `defaultReasoningEffort` per model | `thread/start` and `thread/resume` `reasoningEffort` | `effort` on `turn/start`, kept for later turns |
| Pi | `get_available_thinking_levels` for the current model | `get_state` `thinkingLevel` | `set_thinking_level` |
| ACP (`dsh`) | the `select` config option whose category is `thought_level` | its `currentValue` | `session/set_config_option` |

Levels belong to a model: Claude Code and Codex list them per model, Pi
answers for the selected model, and an ACP agent may change its
`thought_level` option when the model changes.

## Decision

Effort travels with the model list. Adapter and controller negotiate a new
`effort` capability, which requires `models`. When both peers negotiate it, a
`models` frame may carry `effort: {current, options}` for the current model,
bounded to 16 options with the same ID, label, and description limits as
models; `current` is empty while the harness uses its own default. A new
`set_effort` controller frame names one of those option IDs. An adapter that
did not negotiate `effort` reports none, and `capsuled` fails a run whose
adapter sends an un-negotiated effort or answers `set_effort` with
`409 effort_unsupported`.

- `claude-stream` reports the current model's `supportedEffortLevels` and an
  empty `current` until an `apply_flag_settings` request succeeds; after a
  model switch it reports the new model's levels and keeps the chosen level
  when the new model offers it.
- `codex-app-server` keeps each catalog model's levels and default. It takes
  `current` from `thread/start` or `thread/resume`, and sends a change as
  `effort` on the next `turn/start`. When a model switch selects a model that
  does not offer the current effort, the model's default is shown and sent
  with the switch. A model outside the catalog (a provider connection's
  default) reports no levels, so no effort change is offered for it.
- `pi-rpc` asks for `get_available_thinking_levels` after listing models and
  after each successful model switch, and changes the level with
  `set_thinking_level`.
- `acp` reads the `thought_level` select option from the same config option
  lists as the model option and changes it with `session/set_config_option`.
  A list that names the model option replaces the effort option too, so an
  option the agent drops disappears.
- `generic` forwards both when the child negotiates them.

An adapter changes only to a level from its own latest list. Anything else, or
a harness that refuses, is a non-fatal `effort_unavailable` error and the
session keeps its effort.

The daemon exposes `POST /threads/{threadId}/effort` with
`expectedResourceVersion` and `effort`. It persists and delivers the
`set_effort` frame exactly like a model switch: an encrypted transcript block
and the idempotent `appendAndSend` path. A session whose adapter did not
negotiate effort answers `422 unsupported`.

The composer shows the model and the reasoning effort as separate buttons in
the message box (for example "Sonnet" and "High"). Each opens its own menu.
Only the model menu scrolls, with its search field pinned above the list. The
effort menu does not scroll. A list with a single level is not shown, since
it is not a choice, and the effort button is hidden with it.

## Consequences

The operator can change effort per Thread, from the next turn, within the
levels the harness offers for its current model.

Like a model switch, an effort change does not widen what the session can
reach: the endpoint and credential still come from the connection or harness
setup. A higher effort can raise cost and latency.

Effort is not shown for a Codex Thread whose model came from a provider
connection, because Codex has no catalog entry for that model, nor for a Pi
model its configuration does not mark as reasoning. Claude Code does not
report the effort it starts with, so the effort button shows no level until
the operator chooses one.

A coordinated subagent starts with its harness's default effort, as it does
with its default model.

A daemon older than this change does not negotiate `effort`, so a newer
adapter reports none. Official pack images share the daemon's release tag
(ADR 0021).
