# Bring your harness setup

In the UI, select **Import my setup** while creating a Capsule, or open
**Harness settings** in the sidebar. Choose the harness configuration folder or
individual configuration files, select **Review import**, inspect the portable
files and exclusions, then select **Save setup**. The launcher selects the saved setup and
returns you to Capsule creation. No CLI command is required.

For OpenCode, select `~/.config/opencode`. On macOS, use Command-Shift-G in the
picker to enter a hidden folder path. Use the location field or show hidden files
on other systems. Folder selection preserves nested skills and plugins; choosing
individual files is intended for top-level configuration files. More files can be
added before review, and individual selected files can be removed.

Known authentication/runtime filenames and unsupported paths are filtered before
the browser reads their contents. Clicking Review sends the listed files to the
same Meridian installation for in-memory checks. Nothing is saved until Save;
the saved bundle uses the existing encrypted revision storage. The browser does
not expose the absolute source directory, symlink metadata, or executable bits.
Absolute laptop paths may therefore be excluded, and executable scripts can be
marked explicitly in the reviewed file details. Use the CLI when you need its
additional local discovery and path relocation behavior. A folder picker may
require browser-specific permission prompts; individual file selection is the
fallback when folder selection is unavailable.

Reimporting updates the existing default setup with optimistic version checks.
Review shows sanitized file contents, portable npm dependencies and explicit
exclusions. Login caches, conversation history and local services remain outside
the import. Native account sign-in is separate.

## CLI import

Run `meridian harness import` on the computer containing your configuration.
Choose all detected harnesses, or specify `claude`, `codex`, `opencode`, or `pi`.
Meridian shows included files and excluded settings before asking to save.
It never prints imported file contents or imports native login caches.

```console
meridian harness import --preview
meridian harness import codex
meridian --server https://meridian.example --token-file /path/to/token harness import
```

For automation, review `--preview --json`, then use `--yes --accept-exclusions`
to explicitly accept the reported omissions. Imports are bounded to 256 files,
128 KiB per file, and 512 KiB total content. Symlinks and unsupported paths are
excluded. Configuration-directory environment overrides are supported.

Harness settings shows saved setups, their files and revisions, defaults,
rename, rollback, and deletion. Import again to update a default setup. An
interactive `capsule create --harness NAME` notices changes in an existing local
default and offers the same reviewed import before launching.

Choose a Project and New Capsule, select a harness, and click Create Capsule.
The saved default applies automatically. Customize offers clean start, another
setup, a project default, and an optional name. New Capsules receive independent
copies. Updates and rollback apply to future Capsules. Existing session edits
remain local; after runtime recreation, Meridian reapplies the pinned original
revision. Repository files retain the harness's native precedence.

```console
meridian capsule create PROJECT_ID --harness codex
meridian capsule create PROJECT_ID scratch --harness codex --setup clean
```

## Reusable API connections

Enable the gateway on the daemon using an HTTPS origin reachable and trusted
from inside Capsules:

```console
meridiand --provider=docker --provider-gateway-url=https://meridian.example
```

This flag does not configure TLS or expose an extra listener. Your reverse
proxy must forward `/provider-gateway/` to Meridian, disable response buffering
for streaming, preserve Authorization and X-Api-Key, and avoid body/credential
logging. Permit the origin in Capsule egress policy. Permit the control plane
to reach `api.openai.com:443` and `api.anthropic.com:443`. Keep broader control-plane
access restricted using the existing installation authentication.

Add your own OpenAI or Anthropic API key under Provider connections in Harness
settings. Alternatively, pipe a key from your secret manager into:

```console
meridian harness connect openai --stdin
```

The key must come from stdin, never a plaintext command-line argument. On first
launch, choose the connection to authorize it for the Project and harness.
That grant is reused on future launches. Revoke a connection in Settings to stop
active gateway streams and future requests. Replacement keys retain the same
connection identity. Restart active sessions after a daemon restart or expiry
of the 24-hour Capsule lease.

OpenAI Responses and Chat Completions, and Anthropic Messages and token counting
are the supported gateway operations. Other provider operations and arbitrary
upstream endpoints are unavailable. Provider failures are reported without
copying upstream response bodies into error messages.

Subscription sign-in remains inside the unmodified native harness and may need
repeating for new Capsules. Imported settings do not imply that all local tools
will work remotely: laptop endpoints, external binaries, authentication helpers,
and unrecognized settings appear in the exclusion report. Self-contained
skills, hooks, and extensions run inside the Capsule. Supported npx MCP tools
with exact package versions are listed in the import preview and installed
automatically inside the Capsule. Installation disables npm lifecycle scripts,
requires access to registry.npmjs.org, and times out after two minutes. Imported
tools then use the local npm cache offline. Unpinned packages are excluded;
other dependencies must exist in the image or use the harness's supported
installer. Top-level npm versions are pinned, but transitive versions may vary.

Back up the database together with its installation encryption key. Personal
configuration and provider keys are excluded from workspace Moments. Deleted
setups remain retained for pinned Capsules and are unavailable for new selection.

Project dependency preparation can also be reused independently of your personal
configuration. See [Prepared project environments](prepared-environments.md) for
progress, setup settings, rebuild, and retry behavior.

## Manage your saved setup

In Harness settings, Your setups appears before Import from this computer.
Choose **Edit files and skills** to edit configuration, remove a file, or add a
skill directly in Meridian. **Review changes** shows the sanitized files and
exclusions; **Save changes** creates a revision for future Capsules. Existing
Capsules keep their pinned setup. If another edit changed the setup while you
were editing, saving reports a conflict; close and reopen the editor to load the
latest version before reapplying your changes.

Supported browsers use a read-only folder-access picker and skip dependency
folders such as node_modules without scanning their contents. Other browsers
show their standard file-count confirmation before Meridian filters the files;
the importer explains that prompt in advance. No files are sent until review.
Use **Add skills folder** for a folder such as `~/.agents/skills`. Nested files
are mapped into the selected harness's skills folder. Duplicate destinations
must be resolved explicitly.
