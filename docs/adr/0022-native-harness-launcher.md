# ADR 0022: Make the TUI a native harness launcher

- Status: Accepted
- Date: 2026-08-25
- Extends: ADRs 0007, 0013, 0016, and 0021

## Context

Meridian's structured Thread UI speaks only `meridian.adapter.v1`. Making that
UI imitate every OpenCode, Pi, or future harness command would duplicate those
products and blur the adapter boundary. A user entering a Capsule expects the
selected harness itself, including its native terminal UI.

Project harness-image selection happens before the repository is cloned.
Official packs therefore cannot depend on each repository defining the launch
profile that makes the pack usable.

## Decision

- The terminal dashboard is a Project and Capsule launcher. `/new` accepts a
  Capsule name and an applied harness-pack name, creates the Capsule, waits for
  its first native PTY Run, and hands the terminal to that Run.
- Capsule creation resolves the selected name through the Project's
  `harnessImages` allowlist and freezes both the name and image on the Capsule.
  Launcher Capsules do not restore setup-cache Moments, because a cached
  workspace's older immutable image digest must not replace the selected
  harness image.
  Structured Project Thread spawn remains a separate API/CLI workflow and does
  not set a native launcher harness.
- `capsuled` loads trusted image-owned profiles from sorted
  `/etc/meridian/harnesses.d/*.yaml` files and then optional repository profiles
  from `/workspace/.meridian/project.yaml`. Both use the same strict parser.
  Duplicate names fail closed, so repository content cannot shadow an
  image-owned executable.
- When a harness-selected Capsule first becomes Ready, the daemon validates
  that the frozen profile is native and PTY-backed and starts it with a stable
  idempotency key. Any Run history is the durable completion marker; terminal
  Runs are never restarted by recovery. Enter on an idle Capsule explicitly
  starts a later Run.
- Enter never falls back to Meridian Thread chat. Capsules without a frozen
  launcher harness and structured-only or non-PTY profiles fail closed with a
  visible launch error.

## Consequences

The user receives the real harness UI rather than a partial Meridian wrapper.
One live harness still owns a Capsule at a time. `Ctrl-P`, `Ctrl-Q` detaches
without terminating it; `Ctrl-\` and `Ctrl-]` are alternatives. PTY bytes
remain outside encrypted Thread transcripts.

The launcher may set and restore a best-effort local terminal title around the
handoff so the Capsule and harness remain identifiable. Those local OSC writes
occur outside the PTY relay and do not justify parsing, filtering, framing, or
re-rendering native harness output. After writing each complete remote output
frame unchanged, the client reasserts its local Meridian title so a harness
title does not remain authoritative.

Official harness images must package a trusted native profile. Image authors
control executable paths while repositories may add non-conflicting profiles.
The trusted manifest path is an image trust decision, not a new host parsing or
credential channel.

Structured Threads, adapters, and `thread spawn` remain available to API/CLI
clients that deliberately choose the bounded structured protocol.
