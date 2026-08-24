# ADR 0007: Keep harness Runs separate and supervise them in-Capsule

- Status: Accepted
- Date: 2026-08-23

## Context

Harness configuration and execution are repository-controlled and hostile. Prompts and terminal output are content-bearing, while Run identity, state, cancellation, and recovery must remain durable across control-plane restarts. Lifecycle providers do not all support process execution, PTYs, or Git inspection.

## Decision

- A Run is a durable aggregate with a lifecycle independent from its Capsule. Run records contain harness identity, state, timestamps, versions, exit status, and bounded failure metadata, but not prompts or terminal bytes.
- Repository harness YAML is parsed strictly by `capsuled` inside `/workspace`, never by the host. Executables and argument arrays are launched directly.
- Optional Run, attach, and Git runtime ports remain separate from the shared Capsule lifecycle provider interface and are advertised by versioned capabilities.
- `capsuled` owns process groups, timeout/cancellation, PTY allocation, bounded in-memory output replay, and fixed-argument Git inspection.
- Durable control-plane events contain stable ordering and bounded content-free metadata. PTY bytes and full diffs remain transient content responses.
- Public attachment uses short-lived Run-scoped tickets and a WebSocket proxy; private Capsule bearer tokens never cross the public API.

## Consequences

`meridiand` can restart and reconcile a nonterminal Run with a still-running `capsuled` process without replaying its prompt or starting a duplicate process. A `capsuled` process restart loses the in-memory process/replay registry and cannot recover an already-running child; the control plane records that loss rather than fabricating continuity. Output replay is bounded and can explicitly report a cursor gap.
