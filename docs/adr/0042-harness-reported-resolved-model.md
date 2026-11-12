# ADR 0042: Name the model behind a harness's model alias, and report its starting effort

- Status: Accepted
- Date: 2026-11-11
- Extends: ADRs 0031 and 0033

## Context

ADR 0031 shows a structured session's current model in the composer, labelled
with the option the harness lists. Claude Code lists aliases: its rows are
`default` ("Default (recommended)"), `opus` ("Opus"), and so on, and the
composer showed those names instead of the model that runs. ADR 0033 left
Claude Code's effort `current` empty until the operator changed it, because
its `initialize` response lists each model's levels but not the level it
starts on, so the composer showed only "Effort".

Claude Code 2.1.319, as pinned in the official pack, reports both before the
first turn:

- each `initialize` `models[]` row carries `resolvedModel`, the canonical
  model ID the row runs (for example `claude-opus-5-5` for `default`);
- a `get_settings` control request answers with `applied: {model, effort}`,
  the model and effort in effect;
- each turn's `system/init` record carries `model` and `effort`, including a
  change made inside Claude Code such as a typed `/model`.

Codex, Pi, and ACP agents already report a concrete current model and the
current effort (ADR 0033), so they need nothing new.

## Decision

Adapter and controller negotiate a new `resolvedModel` capability, which
requires `models`. When both peers negotiate it, a `models` frame may carry
`resolved`: a display name for the model the current option runs, bounded to
128 bytes with no control characters, like a model label. An adapter that did
not negotiate it reports none, and `capsuled` fails a run whose adapter sends
an un-negotiated `resolved`. The API exposes it on `ThreadModels.resolved`.

The composer's model button shows `resolved` when present and the option's
label otherwise; the menu keeps the harness's own rows. The daemon's model
summary for subagents prefers `resolved` too.

Effort `current` is now the level in effect, including the harness's own
default when the harness reports it, and empty only when it does not say.

`claude-stream`:

- keeps each row's `resolvedModel` and reports the current row's as
  `resolved` from the first `models` frame;
- sends `get_settings` after `initialize` and after each successful
  `set_model`, and takes `applied.model` and `applied.effort` from the answer;
- takes `model` and `effort` from each turn's `system/init` record;
- names a canonical ID such as `claude-opus-5-5` or
  `claude-sonnet-4-5-20250929[1m]` as "Opus 5.5" or "Sonnet 4.5 (1M)". An ID
  in another shape (a cloud provider's ARN-style ID, say) gets no name, and
  the button keeps the option's label;
- reports a level the operator applied over the one Claude Code last
  reported, and clears the reported level on a model switch until
  `get_settings` answers for the new model.

A Claude Code that refuses or does not understand `get_settings` keeps ADR
0033's behaviour: the listed labels and an empty effort `current`.

## Consequences

- The composer names the model and effort a new Thread will use before the
  operator sends anything, so a model or effort choice for the first turn is
  an informed one.
- `resolved` is harness output rendered as text, bounded and stored only in
  the encrypted transcript like the rest of the `models` frame. It is display
  only: switching still names an option ID the adapter reported.
- Each adapter whose harness lists aliases, or does not report its starting
  effort, has to fill these in itself. Today that is only `claude-stream`.
