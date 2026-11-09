# ADR 0034: Declare a custom endpoint model's reasoning effort levels

- Status: Accepted
- Date: 2026-11-08
- Extends: ADRs 0024 and 0033

## Context

ADR 0033 shows and changes reasoning effort for the levels a harness reports
for its current model. For a custom endpoint's model (an `openai-compatible`
or `anthropic-compatible` connection's default model) no harness reports any:

- Codex's catalog never lists the model, so it has no levels for it.
- Pi and DeepSeek Harness treat a model they are not told reasons as
  non-reasoning and offer only `off`.
- OpenCode offers no variants for a model without them.

Self-hosted servers usually do reason, and take an effort level as Chat
Completions `reasoning_effort` or Responses `reasoning.effort`. A
tensorfold server serving GLM-5.3, for example, lists `max`, `high`, and
`low` and honours both fields. The operator knows which levels the model
accepts; the harnesses need to be told.

Each harness already has a native way to declare them, checked against the
pinned pack images:

| Harness | Declaration | Sent upstream |
| --- | --- | --- |
| Pi | `models.json` model `reasoning: true`, `thinkingLevelMap` (a level mapped to `null` is unsupported), `compat.supportsReasoningEffort` | `reasoning_effort` |
| DeepSeek Harness | `llm-pi-ai` route model `reasoningEfforts` (level to wire value, `off` empty) and `compat.supportsReasoningEffort` | `reasoning_effort` |
| OpenCode | provider model `variants`, each with provider option `reasoningEffort`; its ACP agent offers them as the `thought_level` option | `reasoning_effort` |
| Codex | none for the catalog; `model_supports_reasoning_summaries` makes Codex send reasoning settings for an unknown model | `reasoning.effort` |
| Claude Code | lists its own levels for its model aliases | Claude Code's own effort field |

## Decision

A custom endpoint connection with a default model may declare
`reasoningEfforts`: distinct levels from the fixed vocabulary `minimal`,
`low`, `medium`, `high`, `xhigh`, `max`. The daemon validates them, stores
them in canonical order in the connection's metadata, and passes them to
`capsuled` with the model name in the connection lease. `off` is never
declared; every harness offers it and sends nothing for it, which leaves the
server's default. An official connection, or a connection without a default
model, rejects levels.

`capsuled` validates the levels again and writes each harness's native
declaration so that it offers exactly those levels and sends each under its
own name:

- Pi: the model gets `reasoning: true`, `compat.supportsReasoningEffort`, and
  a `thinkingLevelMap` that names every declared level and pins every other
  level to `null`, because Pi treats an absent base level as supported.
- DeepSeek Harness: the route model gets `reasoningEfforts` with `off` empty
  and each declared level mapped to itself, plus
  `compat.supportsReasoningEffort` for Chat Completions routes.
- OpenCode: the model gets `reasoning: true` and one variant per level with
  `reasoningEffort` set to it.
- Codex: `model_supports_reasoning_summaries = true` unless the imported
  configuration sets it, and `MERIDIAN_MODEL_EFFORTS` in the adapter's
  environment. The `codex-app-server` adapter offers those levels for the
  model the Thread opened with when Codex's catalog lacks it; catalog models
  keep their own levels.
- Claude Code: unchanged.

The CLI takes `--effort low,high,max`, and the Harness settings form shows a
checkbox per level once a default model is set.

## Consequences

The composer's effort picker (ADR 0033) works for self-hosted models in Pi,
DeepSeek Harness, OpenCode, and Codex.

The levels are operator configuration, not credentials. Like the model name,
they reach the Capsule; the base URL and key still do not. A wrong
declaration only makes the harness send a level the server rejects, which
fails that turn.

Imported personal setups keep their own model declarations: the connection
writes only the `meridian` provider or route it owns.
