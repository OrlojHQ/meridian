# ADR 0012: Structured thread transcript encryption

## Status

Accepted.

## Context

Normal Runs intentionally keep prompts and PTY traffic transient. Structured
agent threads need durable, ordered history so an explicit conversation can be
resumed, but that history can contain source, credentials, tool output, and
prompt-injection material. SQLite backups must remain useful without turning an
installation key into another copied database secret.

## Decision

Meridian stores explicit structured threads as Capsule-owned aggregates:

- at most one `active` thread exists for a Capsule, while paused, archived, and
  crypto-shredded histories remain retained;
- messages and typed blocks are append-only, strictly sequenced, and immutable;
- bounded adapter metadata is retained only inside the encrypted block payload,
  never in plaintext Thread metadata or lifecycle events;
- thread lifecycle and adapter/run references use optimistic resource versions;
- a current Run must belong to the same Capsule as its thread; and
- no Run prompt, PTY byte stream, or implicit harness traffic becomes a
  structured transcript.

Each thread receives a cryptographically random 256-bit data-encryption key
(DEK). Block plaintext is encrypted with AES-256-GCM and a fresh 96-bit random
nonce. Versioned authenticated additional data binds the Thread and Message
identities, message and block sequence, role, message/block kinds, and immutable
creation timestamp. The DEK is wrapped with the installation key-encryption key
(KEK) using AES-256-GCM and AAD that binds Thread identity and KEK identity and
version.

The installation KEK is a separate mode-0600 regular file in a mode-0700
directory. Key-file creation is same-filesystem atomic and refuses a symlink at
the key or key-directory path. The default is
`<data-dir>/transcript-keys/installation.key`; it is outside SQLite and is
deliberately omitted from Meridian backups. Operators may configure a separate
path.

Key rotation is explicit and offline through
`meridian admin transcript-key rotate`. A restrictive transitional keyring
keeps old and new KEKs available across crash boundaries; one SQLite
transaction authenticates and re-wraps every non-deleted Thread DEK, deep
verification precedes atomic old-key retirement, and failures retain rollback.
Automatic or silent key substitution is forbidden.

Each outbound persisted user message has a content-free durable delivery
record containing only Thread, Message, Run, stable controller identity, and
acknowledgement time. Client idempotency-key replay delivers a pending message
after a crash-before-send and skips an acknowledged message. `capsuled`
controller-ID deduplication is defense in depth for the unavoidable
send/acknowledgement boundary, not the application idempotency mechanism.
Daemon recovery reconciles runtime state but never sends durable messages;
delivery requires explicit client mutation retry.

Crypto-shred deletion transitions the Thread to `deleted`, clears its wrapped
DEK, current Run, and opaque adapter state, and retains only content-free audit
metadata plus unreadable immutable ciphertext rows. This does not claim
physical media erasure.

Block, message, and thread quotas are checked before encryption or database
allocation. Errors use content-free corruption/key-mismatch categories.
Transcript content is forbidden from logs, events, metrics, traces, and error
messages.

Backups contain ciphertext and wrapped DEKs. Their manifest lists required KEK
IDs/versions but contains no KEK material. Checksums and SQLite integrity can be
verified without a key; optional deep verification authenticates every retained
envelope with a separately supplied key. A restore containing retained
transcripts fails before replacement unless the supplied or preserved
installation key authenticates them.

## Consequences

Loss of the installation KEK permanently makes retained transcript ciphertext
unrecoverable. Database-only compromise does not disclose transcript plaintext,
but a process or host compromise that can read both SQLite and the KEK can.
Metadata such as IDs, roles, kinds, timestamps, counts, adapter identity, and
Capsule relationships remains visible in SQLite and backups.

The trusted local daemon and user remain inside the confidentiality boundary:
`meridiand` holds the KEK and explicit Thread reads return decrypted content.
This design protects data at rest, not against daemon, host, or loopback-API
compromise.

AES-GCM nonce uniqueness depends on the operating system CSPRNG. The design
does not derive nonces, reuse a DEK between Threads, or invent a cryptographic
construction. Key rotation and backup-key custody are operational obligations,
not substitutes for host access control or encrypted backup media.
