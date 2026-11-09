# ADR 0032: Let ACP agents ask the operator questions

- Status: Accepted
- Date: 2026-11-08
- Extends: ADRs 0013 and 0030
- Amends: ADR 0030 (form-mode elicitation is no longer refused)

## Context

ADR 0030 refused every ACP agent-to-client request except
`session/request_permission`, and said that offering any client capability
needs its own decision. An ACP agent therefore cannot ask the operator a
question, even though Claude Code (`AskUserQuestion`), Codex
(`requestUserInput`), and Pi (extension UI) can, through the
`input_request` and `permission_request` frames they already use.

ACP defines `elicitation/create`, which a client opts into with the
`elicitation` client capability. It has two modes:

- **form**: a message and a restricted JSON Schema whose properties are flat
  `string` (optionally single-select through `enum`, legacy `enumNames`, or
  `oneOf` const entries, with `format`, length bounds, and `pattern`),
  `number`, `integer` (with `minimum` and `maximum`), `boolean`, and `array`
  (multi-select over string choices, with item bounds). The client answers
  `accept` with `content`, `decline`, or `cancel`.
- **url**: the client opens a URL for an out-of-band flow and later receives
  `elicitation/complete`.

A request is either session-scoped (`sessionId`, optional `toolCallId`) or
request-scoped (`requestId`, outside a session).

No shipped ACP pack sends `elicitation/create` today: the `dsh` package
depends on the ACP SDK but never calls `createElicitation`, and OpenCode 2's
ACP agent does not send it. The decision lets any future ACP harness ask
questions without new driver code.

## Decision

The `acp` driver advertises `{"elicitation": {"form": {}}}` in `initialize`
when permissions are enabled, and declares `inputRequests` in its hello. With
`--permissions=false` it advertises neither and declines every elicitation.

It never advertises URL mode. A URL elicitation, a request-scoped
elicitation, an elicitation for another session, and a form whose schema it
cannot ask faithfully are all answered `decline` without reaching the
operator. A request that arrives after cancel is answered `cancel`.

A form is asked through the frames the browser and TUI already render, one
field at a time in the schema's declared order, so declining any field ends
the request and no other question is left open:

- A form without properties is a confirmation: a `permission_request` of kind
  `question` with **Accept**, **Decline**, and **Cancel**.
- A `boolean`, or a single-select `string` with at most 30 choices, is a
  `permission_request` of kind `question` with one button per choice
  (**Yes**/**No** for a boolean), then **Skip** when the field is optional,
  then **Decline**.
- Every other field is an `input_request` whose prompt says what the answer
  must look like. Optional fields accept `-` to leave the field out.

Every question repeats the agent's message, numbered "(n of m)" when the form
has more than one field. Each typed answer is checked against the field's
schema in the adapter: numbers and integers are parsed and range-checked,
choices must match a choice's value or label, multi-select answers are
comma-separated choices with item bounds, strings are length-checked and
validated for the `email`, `uri`, `date`, and `date-time` formats, and a
`pattern` is applied with Go's linear-time RE2 engine when it compiles. An
answer that does not fit is asked again with the reason. Only when every
field has an accepted answer does the agent receive `accept` with typed
content.

A button choice the question did not offer declines. Cancel, and
`$/cancel_request`, answer open elicitations as they answer open permission
requests.

Bounds: at most 16 properties with names up to 256 bytes and unique keys,
string choices limited as option labels are, and patterns up to 1 KiB. A
schema with an unknown property type, an unsupported string `format`, an
array of anything but string choices, an empty numeric range, or a negative
bound is declined.

The protocol, daemon, API, and UI are unchanged except that the UI styles a
**Decline** answer as a denial.

## Consequences

ACP agents can ask the operator questions, and the answers they receive
always match their own schema.

Answers are operator text delivered to a hostile agent and stored, like every
input answer, in the encrypted transcript. ACP has no secret-field marker,
so the adapter cannot mask an answer; an agent can ask for a credential, and
the operator must not provide one. This is the same exposure as an ordinary
user message.

URL-mode elicitation stays unsupported: opening an agent-chosen URL in the
operator's browser would let a hostile agent drive phishing or OAuth flows
outside the Capsule.

A long form takes one answer per field. Collecting several fields in one
card would need a new frame shape and is not part of this decision.
