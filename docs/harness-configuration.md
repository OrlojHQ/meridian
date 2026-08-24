# Harness configuration

Generic executable profiles live in
`/workspace/.meridian/project.yaml`. `capsuled` reads and validates this file
inside the Capsule; `meridiand` never parses repository-controlled YAML on the
host.

```yaml
version: v1
harnesses:
  - name: example
    executable: /usr/local/bin/example-harness
    arguments: ["--format=jsonl"]
    workingDirectory: .
    promptMode: stdin
    pty: false
    outputMode: jsonl
    timeout: 30m
    secretReferences: []
```

Fields are strict:

- `name` is unique.
- `executable` is invoked directly. `arguments` remains an argument array and is never concatenated into a shell command.
- `workingDirectory` is a clean relative path beneath `/workspace`; absolute and escaping paths are rejected.
- `promptMode` is `argument`, `stdin`, or `interactive`. Argument mode appends one argument, stdin mode writes once and closes stdin, and interactive mode requires a PTY.
- `pty` requests a real reconnectable PTY.
- `outputMode` is `text` or `jsonl`. JSONL records are parsed into bounded metadata; malformed or oversized lines produce bounded parse-error events.
- `timeout` is positive and no greater than 24 hours.
- `secretReferences` contains opaque metadata names only.

Unknown fields, malformed YAML, duplicate names, inline secret-like values, unsafe paths, unreasonable sizes, and invalid modes fail closed. Secret references are not resolved: a profile containing any reference fails explicitly when started. Do not put plaintext credentials in this file.

Structured profiles use the same `v1` configuration schema and select a
separate, non-PTY adapter process:

```yaml
version: v1
harnesses:
  - name: structured-example
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/example-adapter
      arguments: ["--driver", "example"]
    workingDirectory: .
    pty: false
    timeout: 30m
    secretReferences: []
```

Omitting `interactionMode` preserves the existing `native` behavior exactly.
A structured profile cannot set native `executable`, `arguments`,
`promptMode`, or `outputMode` fields and cannot request a PTY. Its adapter is
executed directly, with the configured argument array and validated
workspace-contained working directory. Only `meridian.adapter.v1` is accepted.
Inline credential-shaped adapter arguments are rejected; unresolved
`secretReferences` fail closed when the session starts.

The adapter protocol is bounded LF-delimited JSON, not generic JSONL:
frames require the exact protocol version and a known frame type, CRLF and
unterminated records are rejected, and every frame/content/tool/result/metadata
allocation has an explicit limit. Unknown frame types fail the session;
forward-compatible display metadata belongs in the bounded `metadata` object.
Structured sessions use dedicated authenticated private Capsule endpoints and
never share the PTY WebSocket.

The packaged adapter binary does not install OpenCode, Pi, model providers, or
credentials. Those remain responsibilities of the selected project image and
its controlled runtime environment. Adapter command arguments and repository
YAML are not credential channels.

Harnesses and repository setup code are hostile. They run as UID 10001 inside the selected trusted-development Capsule and can modify its workspace. Output, terminal bytes, and Git diffs are also hostile and may contain secrets or terminal escape sequences. Meridian bounds these streams and omits their content from normal durable events and logs, but an attached client or explicit diff caller receives the content and must handle it safely.

No proprietary harness is required or implied. The integration suite uses `testing/mock-harness`, a deterministic network- and credential-free fixture binary.
