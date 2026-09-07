# ADR 0024: Personal harness setups and scoped provider connections

- Status: Accepted
- Date: 2026-09-06

## Decision

Personal harness configuration is installation-owned, explicitly imported by
an authenticated local CLI, and stored as encrypted immutable revisions.
It is separate from Capsule-local executable profiles. A personal default or
explicit project override is resolved in the Capsule creation transaction;
existing Capsules retain their pins after updates, rollback, or deletion.
A clean launch imports no personal setup. Native and structured runtime starts
use the same private materialization protocol.

Imports inspect bounded, known harness roots using rooted filesystem access.
They do not execute local hooks, credential helpers, plugins, or MCP servers.
Authentication caches, histories, trust decisions, unknown configuration fields,
unsafe paths, and unsupported local dependencies are excluded with a report.
Text scanning is defense in depth, not proof that arbitrary user-authored files
are secret-free. Users review imports before transfer. All bundle content is
encrypted; lifecycle and revision metadata contain no file contents.

The Capsule supervisor installs configuration into its bounded private home,
using rooted operations and atomic directory installation. It never installs
host mounts. Runtime edits stay in that Capsule and are not synchronized back.
After runtime loss, the original pinned revision is rehydrated. Workspace
Moments do not include personal homes, provider keys, or login caches.

Reusable provider connections support OpenAI and Anthropic API keys. They are
separate encrypted records, authorized per project and harness. The gateway
accepts only scoped, expiring Capsule credentials and fixed inference routes;
it never forwards client-supplied authorization, cookies, or arbitrary URLs.
It does not log prompts, responses, or API keys. Revocation cancels active
streams and rejects new requests. API-key replacement retains the connection
identity so new requests can use the replacement without a setup revision.

The gateway requires an explicitly configured HTTPS Meridian origin reachable
from Capsules. The deployment operator supplies TLS, routing, and network
policy. Capsule leases are memory-only and expire within 24 hours; daemon
restart invalidates them, so active harness sessions must restart to reconnect.

Subscription authentication remains the native harness's responsibility.
Meridian does not import, store, or synchronize subscription token caches.
Remote authenticated MCP transports, arbitrary cloud-provider credentials,
and laptop tool forwarding are not implied by API-provider connections.

## Consequences

One import serves future independent Capsules, including on a personal remote
installation. Setup revisions and connection envelopes join the existing
installation database/key backup boundary. Capsule isolation remains the
selected provider's boundary; this feature does not make local Docker suitable
for hostile multi-tenancy.

Portable source can include executable hooks and extensions. These execute
only inside a Capsule when its harness loads them; they are hostile code and
inherit the Capsule's limits. Exact-version npm packages referenced by supported
npx MCP definitions are installed inside the Capsule with lifecycle scripts
disabled and a two-minute timeout. Imported npx definitions use offline resolution
after preparation. Optional harness packs include a checksum-pinned Node runtime;
the thin supervisor image does not. The Capsule needs npm registry access during
preparation. Top-level versions are pinned; transitive dependencies are resolved
by npm. Other dependencies must already exist in the image or be handled by the
harness's supported extension installer; laptop services are excluded.

## Browser import

The dashboard and native launcher accept explicitly selected local files through
browser file/folder inputs. They do not discover the laptop filesystem remotely or
introduce a local agent. Filename filtering happens before browser content reads;
the authenticated preview endpoint independently applies the portable path allowlist,
configuration sanitization, reference pruning, credential-shaped content checks and
bundle validation. The original upload is bounded and processed only in memory,
with no-store responses. Saving remains a separate explicit, idempotent import
using encrypted immutable revisions. Preview receives no server filesystem paths
to open and executes no imported code. The CLI remains available for native path
and permission discovery that browser file APIs do not expose.

### Saved setup editing and directory access

The authenticated dashboard can explicitly read the current saved bundle through
`GET /harness-setups/{setupId}/contents`. The response includes the corresponding
resource version from the same store read and uses `Cache-Control: no-store`.
Decryption uses the installation key; this endpoint does not read
provider-connection credentials. The dashboard retains contents only in editor component state,
not browser storage or the query cache. Edits pass through the existing preview
sanitizer and explicit review before an encrypted revision is saved using the
loaded resource version. Existing Capsule pins remain immutable.

Where available, browser directory access uses a read-only handle scoped to an
explicitly selected folder. The importer applies its allowlist before descending
into directories, skipping node_modules and authentication files before access.
Handles are not persisted. Other browsers retain the file input fallback with an
explanation of its browser-controlled total count. Additional skill folders are
explicit selections, mapped to the chosen harness's skills directory; duplicate
destinations are rejected rather than silently overwritten.
