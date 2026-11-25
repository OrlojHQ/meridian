# ADR 0064: Seamless Capsule image upgrades and persistent session homes

- Status: Accepted
- Date: 2026-11-24
- Amends: ADR 0041 (official packs follow the release), ADR 0019 (private home lifetime),
  and ADR 0062 (Antigravity profile storage)
- Extends: ADRs 0005, 0010, 0044, 0050, and 0059
- Tracks: #267

## Context

ADR 0041 moves every **new** Capsule onto the current release's official pack
image when `meridiand` updates, while leaving existing Capsules pinned to the
image digest they were created with until they are deleted.

That creates two problems once Capsules live for days or weeks — especially
standing project leads (ADR 0059):

1. **Capsule-side fixes and features never reach live Capsules.** Each pack
   image carries `capsuled`, the harness adapter, `meridian-coord`, its tool
   descriptions, and the harness binary. After a Meridian upgrade, an operator
   has to discard and recreate their long-lived Capsules to receive security
   fixes, new `gh` / GitHub App wrappers (ADR 0061), or updated coordination
   tools and briefs.
2. **Every container or Pod restart already wipes harness session memory.**
   Today, both providers mount `/home/capsule` ephemerally — a `256m` `tmpfs`
   in Docker and a `512Mi` `EmptyDir` in Kubernetes Agent Sandbox. An audit of
   all six official packs shows that every harness stores its session files
   under `/home/capsule` (either directly, such as Pi's
   `/home/capsule/.pi-sessions`, or inside `$HOME` / `$XDG_DATA_HOME` under
   `/home/capsule/.meridian-setup-<rev>`):
   - Claude Code (`claude`): `$HOME/.claude/`
   - Codex (`codex`): `$CODEX_HOME` (`$HOME/.codex/`)
   - OpenCode (`opencode`): `$XDG_DATA_HOME/opencode/`
   - Pi (`pi`): `/home/capsule/.pi-sessions`
   - DeepSeek Harness (`dsh`): `$HOME` / `$XDG_DATA_HOME`
   - Antigravity (`agy`): `$HOME/.gemini/antigravity-cli/`

   Because `/home/capsule` disappears on every stop or Pod replacement, an
   in-place image swap (and even a Docker Desktop restart under ADR 0044)
   wipes the harness's on-disk session state and breaks session resume.

An audit of `sigs.k8s.io/agent-sandbox@v0.5.6` also shows two constraints that
shape Kubernetes and microVM (Kata/gVisor, ADR 0050) upgrades:

- `SandboxReconciler.reconcileExistingPod` does not replace a running Pod when
  `Sandbox.Spec.PodTemplate.Spec.Containers[0].Image` changes; it only patches
  metadata. Setting `Spec.OperatingMode` to `Suspended` deletes the Pod while
  keeping the `Sandbox` and its PVCs, and setting `OperatingMode` back to
  `Running` creates a new Pod from the updated `PodTemplate` attached to the
  same PVCs. This uses only Meridian's existing `sandboxes: update` RBAC.
- `Sandbox.Spec.VolumeClaimTemplates` is immutable after creation (enforced by
  CRD CEL validation). Persistent home PVCs therefore apply to newly created
  Sandboxes, while pre-existing Sandboxes upgrade cleanly without adding a PVC.

Finally, upgrading an existing Capsule differs from creating one: `/workspace`
already holds the operator's branch, uncommitted edits, and build outputs, and
`.meridian-setup-complete` is keyed by the creation image digest (ADR 0019,
ADR 0041). Re-cloning, restoring a setup Moment, or rerunning Project `setup`
on a dirty working tree would risk clobbering work.

## Decision

### 1. Background pre-pull of allowlisted official packs

When `meridiand` starts on a new release and the configured provider supports
host-side image pulls (Docker), the daemon inspects existing Projects, collects
the distinct official pack references allowlisted under ADR 0041, resolves them
to the current release catalog, and pulls any missing images in the background:

- Concurrency is bounded (at most 2 pulls at once) and cancelled cleanly on
  daemon shutdown.
- Only official packs already allowlisted by at least one Project are pulled;
  unused packs are not downloaded.
- Pull failures are logged with bounded error codes (same classification as
  Capsule startup pulls) and never block daemon startup or Capsule creation.

Kubernetes / Agent Sandbox relies on node-side kubelet pulls, so pre-pull is a
no-op there.

### 2. Persistent Capsule home and ephemeral `/run/meridian-session` mount

Separate durable harness session state from ephemeral Meridian credentials and
from the Agent Sandbox bearer token mount (`/var/run/meridian/token`, which on
Debian Bookworm resolves through the `/var/run` → `/run` symlink):

- **Persistent `/home/capsule` volume:**
  - **Docker:** Each Capsule gets a second owned Docker volume
    (`names.homeVolume`) mounted at `/home/capsule` (`uid=10001`, `gid=10001`),
    alongside `names.volume` at `/workspace`. Capsule deletion removes both
    volumes. Pre-existing Capsules created before this change receive a new
    `home` volume on their first upgrade.
  - **Agent Sandbox (containers, gVisor, and Kata microVMs):** New Sandboxes
    add a second `VolumeClaimTemplates` entry (`home`, `ReadWriteOnce`, `2Gi`
    default, configurable via Helm) mounted at `/home/capsule` instead of
    `EmptyDir`. Because `volumeClaimTemplates` is immutable, pre-existing
    Sandboxes keep their `EmptyDir` and still support image upgrades (falling
    back to a fresh harness session on upgrade).
  - Setup Moments (ADR 0019), workspace Moments, local sync, and Deliveries
    continue to capture **only `/workspace`**, never `/home/capsule`.
- **Ephemeral `/run/meridian-session` `tmpfs`:**
  - Both providers mount an ephemeral memory-backed directory at
    `/run/meridian-session` (`rw,exec,nosuid,nodev,size=16m,uid=10001,gid=10001,mode=0700`
    in Docker; `EmptyDir` with `Medium: Memory`, `16Mi` in Agent Sandbox).
    This path never aliases `/var/run/meridian/token`.
  - Short-lived credentials and wrappers move off `/home/capsule` onto
    `/run/meridian-session`:
    - GitHub App installation token file (`github-token`),
      `git-credential-github`, and the `gh` wrapper (ADR 0061);
    - one-shot clone `GIT_ASKPASS` helpers.
  - These files never touch a persistent volume or PVC.
- **Resetting manifest-owned harness setup paths once per `capsuled` boot:**
  - Today `materializeSetupHome` skips once `.meridian-setup-<rev>/.complete`
    exists, which meant a prompt-injected turn that modified a setup-owned
    config (such as Claude `settings.json` or Codex `config.toml`) only lasted
    until the next restart. With a persistent home, it would survive every
    later release.
  - The first session after `capsuled` starts removes every path the harness
    manifest owns (its root `paths`, `shared` directories, and `homeFiles`,
    such as Claude's `settings.json`, `hooks/`, and `.claude.json`) inside
    `.meridian-setup-<rev>` through `os.OpenRoot`, whether or not the setup
    bundle ships that path, then rewrites the bundle. A hook, MCP server, or
    plugin planted at one of those paths therefore lasts no longer than it
    did on a `tmpfs` home. Later sessions in the same process reuse the home
    and keep their edits, as before. Harness session files live outside the
    manifest paths and survive.
  - A Run with no saved setup and no provider connection runs its harness
    with `HOME=/home/capsule` itself. `capsuled` therefore also removes every
    manifest-owned path of every harness directly under `/home/capsule` when
    it starts, before serving, so configuration planted there is reset on
    each boot too.
- **Antigravity profile storage and scrubbing (amends ADR 0062):**
  - During implementation, verify whether `agy` honors a config-directory
    override pointing into `/run/meridian-session`. If it does, write the
    OAuth profile only to that `tmpfs`.
  - If `agy` requires `$HOME/.gemini/antigravity-cli/` on the persistent
    volume, `capsuled` scrubs `antigravity-oauth-token` and
    `cache/acp-auth-state.json` across `/home/capsule` and every
    `.meridian-setup-*` directory on supervisor boot (before serving) and on
    session end/revocation, rewriting them only when a session starts with an
    authorized `antigravity` connection. `docs/threat-model.md` records the
    resulting host-disk and PVC-backup (e.g., Velero) exposure.
- **Storage bounds and cleanup:**
  - Tool caches stay in the setup home. Setup dependencies are cached in its
    `.npm` for `npx --offline` MCP servers, and harness runtimes load native
    code from `XDG_CACHE_HOME`, which the `noexec` `/tmp` cannot serve.
  - On supervisor boot and upgrade, `capsuled` prunes stale
    `.meridian-setup-*` directories that do not match the active setup revision
    (preserving session state for the active revision) and clears stale files
    in `$HOME/.meridian/coord` (which also remains capped at 16 MiB per lead
    during runtime).
  - `ListenCoordination` already unlinks a stale `$HOME/.meridian/coord.sock`
    socket on startup (`internal/capsuleproto/coordination.go`).
  - Because local Docker volumes have no built-in per-volume quota, boot/idle
    pruning keeps persistent home growth
    bounded to harness session logs.
  - After a Docker swap, the provider removes, in the background and without
    force, the one tag the Capsule ran before, and only while that tag still
    names the image the old container ran. Docker refuses to remove an
    image's only tag while any container, running or stopped, still uses it,
    and an image other tags still name stays; a later Capsule that needs a
    removed image pulls it again.

### 3. Seamless automatic upgrades when idle, with a manual fallback

Upgrading existing Capsules should feel automatic so operators do not have to
recreate Capsules after updating Meridian, without interrupting active work or
stalling user messages on slow Pod/image pulls.

- **Eligibility:** A Capsule is eligible only when:
  - its Project harness resolves as an official pack under ADR 0041 (matches a
    catalog pack, same registry/repository, no digest pin, release tag);
  - the daemon's current catalog reference differs from the Capsule's recorded
    `ImageReference` (or, for local/dev tags on the same ref, only when
    explicitly requested); and
  - the provider advertises `Capabilities.UpgradeImage`.
  Custom images, custom tags, and digest-pinned references are never upgraded.
- **Quiescence requirement:** A Capsule may be swapped only when its state is
  `Ready` or `Paused` (matching `DesiredState`), `Maintenance == ""`, and it is
  strictly idle:
  - no active Run (including native terminal/PTY runs);
  - no active structured turn or pending operator permission/question;
  - no active Delivery, Project Check, local sync, or open preview tunnel; and
  - for a lead Thread, **no active or starting subagents** and **no unread
    subagent reports or pending wakes** in its mailbox.
- **Automatic triggers (never synchronously inside a Thread send):**
  - **Paused Capsules:** When `meridiand` reconciles a `Paused` eligible
    Capsule:
    - On Agent Sandbox, the Pod is already deleted while `Suspended`, so
      updating `Sandbox.Spec.PodTemplate.Spec.Containers[0].Image` happens in
      place with zero Pod churn; the next Resume boots directly into the new
      image.
    - On Docker, once the new image is pre-pulled, the reconciler swaps the
      stopped/paused container onto the new image in the background (leaving
      its desired state `Paused`).
  - **Background idle reconciliation (`Ready` Capsules):** A bounded background
    reconciler pass upgrades eligible `Ready` Capsules that meet the full
    quiescence check above (and, on Docker, only after the new image is
    already present locally from pre-pull).
  - **Never block a Thread send on an upgrade:** Sending a message to a Thread
    never initiates an image swap inline. If a user message, subagent report,
    or Routine wake arrives while a background upgrade is already in flight
    (`Maintenance == "upgrading"`), `meridiand` **queues/buffers** it (in the
    Thread queue or durable coordination mailbox) and delivers it once the
    upgrade finishes rather than rejecting it with `409`.
  - **Busy Capsules:** Capsules with an active turn, running subagents, open
    terminal, or active preview are left alone until idle. A background pass
    also skips a `Ready` Capsule with any recorded activity (a Run, a preview
    request, a file or terminal operation) in the last 15 minutes, and
    re-checks that once the upgrade holds the Capsule, before stopping an idle
    session (stopping it records activity itself), so a dev server or
    background process the operator is still using is not restarted. An
    operator's own upgrade has no grace period. The UI/CLI also
    offers an explicit manual action (**Upgrade to vX.Y.Z** /
    `meridian capsule upgrade <id>`) that runs once the Capsule is idle.
- **Workspace untouched (skip preparation entirely):** The upgrade path skips
  the `Prepare` / staged-preparation RPC entirely (same as ADR 0044 restart).
  It mounts the existing `/workspace` volume/PVC as-is and leaves
  `.meridian-prepared` and `.meridian-setup-complete` untouched, so a digest
  key mismatch in `.meridian-setup-complete` never triggers a re-clone, setup
  Moment restore, or Project `setup` rerun over an existing workspace.

### 4. Provider contract, swap sequence, and rollback

Extend `ports.ProviderCapabilities` with `UpgradeImage bool` and add a provider
method `UpgradeImage(ctx, resourceID, imageReference, reportStartup)` (reusing
the existing startup progress phases):

1. Set `capsule.Maintenance = "upgrading"` under a transaction. Destructive or
   mutating runtime operations (Delivery, Pause, Delete, new native Run) wait
   or return `409`; incoming Thread sends, subagent reports, and Routine wakes
   buffer in their durable queues until `Maintenance` clears.
2. Re-verify official-pack eligibility (ADR 0041) and Project isolation class
   (ADR 0050) fail-closed before touching the provider.
3. **Pull before stopping (Docker):** Resolve and pull the new image while the
   old container and any idle session are still intact, before the upgrade
   claims the Capsule. If the pull fails, the Capsule and its session are
   untouched on the old image.
4. Stop any idle structured session process cleanly before swapping the
   runtime. If it does not stop cleanly the upgrade is refused, and the
   session is resumed only once the runtime confirms the old session has
   ended, so two harness processes never share one session; otherwise the
   Thread stays Paused for the operator to resume.
5. **Swap the runtime:**
   - **Docker:** Create the replacement container under a temporary name
     (`<name>-upgrade`) with the new image digest, pointing at the existing
     `names.volume`, `names.homeVolume` (creating `names.homeVolume` if
     absent), and `names.network`. Stop the old container (so two `capsuled`
     processes never touch the volumes concurrently), start the replacement,
     and wait for `capsuled` readiness, for Paused Capsules too. Once ready,
     rename the old container to `<name>-previous`, rename `<name>-upgrade` to
     `<name>`, then remove `<name>-previous`, so some container holds the
     Capsule's name at every step. A Paused Capsule's replacement is stopped
     again; a stopped runtime is all Paused promises (ADR 0044).
   - **Agent Sandbox:** Re-verify the RuntimeClass (ADR 0050), update
     `Sandbox.Spec.PodTemplate.Spec.Containers[0].Image`, and give a Pod
     template created before this ADR the `/run/meridian-session` volume,
     mount, and `--session-dir` flag (`podTemplate` is mutable; only
     `volumeClaimTemplates` is not). If the Sandbox is
     `Running`, transition `OperatingMode` to `Suspended` (wait until the old
     Pod terminates) then back to `Running` (wait for the new Pod and
     `capsuled` readiness). If the Sandbox is already `Suspended`, update the
     image in place without starting a Pod.
6. **Rollback on boot failure:**
   - **Docker:** Stop and remove `<name>-upgrade`, then `docker start` the
     untouched old container and wait for `capsuled` readiness. A failed
     second rename first gives the old container its name back.
   - **Agent Sandbox:** Restore the previous Pod template on the `Sandbox`
     and cycle `Suspended` → `Running` (if it was `Running`).
   - After any failure the provider's `RecoverUpgrade` settles the runtime
     (removing leftover `-upgrade` / `-previous` containers) and reports
     whether it runs the target image; the record follows it. Only if that
     also fails does the Capsule transition to `Failed` (with `/workspace`
     intact). The reconciler, which skips Capsules in `upgrading` maintenance
     so it never restarts a runtime the swap stopped, then checks the
     runtime.
   - A daemon restart mid-upgrade runs the same settlement for every Capsule
     left in `upgrading`, before the reconciler recovers Capsules.
   - One upgrade runs per Capsule at a time, and the full quiescence check
     (except the idle Thread session itself) runs before that session is
     stopped. The stopped session is resumed after the upgrade, whether it
     succeeded or not, when the Capsule is still Ready, and only then are
     waiting Thread sends released. A send made with the Thread version from
     before the upgrade applies to the resumed session instead of failing
     with a version conflict.
   - An upgrade that settlement finds finished is recorded like one that
     finished in place, so the next session may still fall back to a fresh
     one and a lead's mailbox is pushed to the new `capsuled`.
7. On success, update the Capsule's `ImageReference` and
   `Preparation.ImageDigest`, clear `Maintenance`, record a
   `capsule.image_upgraded` event (old/new refs and digests), and flush any
   queued Thread sends, subagent wakes, or Routine wakes.

### 5. Harness session continuity, brief refresh, and fallback

- **Seamless resume by default:** Because `/home/capsule` persists across the
  swap, the next turn sends `KindResume` as usual. The harness resumes its
  existing conversation with the new `capsuled`, new `meridian-coord` binary
  and tool descriptions, and re-delivered working/project notes.
- **Brief refresh on resume:**
  - Claude Code (`--append-system-prompt`), Codex (`developerInstructions` on
    `thread/resume`), and Pi (`--append-system-prompt`) already pass the
    current brief on every resumed launch.
  - Extend the ACP adapter's `ResumeState` to record a short digest of the
    delivered brief alongside `role` so ACP harnesses (`opencode`, `dsh`,
    `agy`) re-deliver `<meridian-context>` when a release changes the brief
    text.
- **Normalize Pi resume failure reporting:** Today `pirpc.go` emits
  `pi_resume_invalid` on malformed resume state and a non-fatal
  `pi_command_failed` when `switch_session` fails. Extend `pirpc.go` so a
  failed startup `switch_session` fails the session with `pi_resume_failed`,
  matching `claude_resume_failed`, `codex_resume_failed`, and
  `acp_resume_failed`.
- **Graceful fresh-session fallback:** If the first `KindResume` after an
  upgrade fails with a session-resume failure (`claude_resume_failed`,
  `codex_resume_failed`, `acp_resume_failed`, `pi_resume_failed`, any
  `*_resume_invalid`, or `acp_resume_unsupported`) — for instance, on a legacy
  Capsule's one-time migration off `tmpfs`/`EmptyDir`, when a major harness CLI
  upgrade breaks its session schema, or when an older pack's adapter rejects
  resume state a newer one wrote (ACP state decodes strictly, so the new
  `briefDigest` field is unknown to it after a Meridian downgrade) — Meridian
  retries once with `KindStart` on the **same
  Thread**. The Capsule, Thread transcript, standing-lead coordination grant,
  subagent tree, and working/project notes all survive, and Meridian inserts a
  single quiet transcript divider (*"Upgraded Capsule from vX.Y.Z to vA.B.C —
  harness started a fresh session"*).

### 6. Moments, subagents, and VM / microVM compatibility

- **Moments and lineage:** Past Moments keep the `ImageDigest` recorded when
  they were captured; future Moments capture the new `ImageDigest` from the
  provider. Forking or rewinding from an older Moment continues to use that
  Moment's recorded digest.
- **Subagents:** Upgrading a lead never cascades automatically to its
  subagents. Subagents are short-lived and may be mid-task; each Capsule
  upgrades independently when idle.
- **MicroVMs and future VM providers:** Kata microVMs under Agent Sandbox
  (`kata-qemu`, `kata-clh`, `kata-dragonball`) use the exact Agent Sandbox
  `UpgradeImage` path above — updating the OCI pack image while keeping the
  `workspace` and `home` PVCs attached. The Kata guest kernel remains an
  operator-managed node artifact (ADR 0050). Any future direct-VMM or
  block-backed provider (`kata-fc`, #86) implements the same `UpgradeImage`
  contract: discard any VM memory snapshot (suspend-to-disk), boot the new pack
  image against the same persistent disks, and resume the harness from disk.

## Consequences

- Operators who update Meridian get capsule-side fixes, new `capsuled`
  protocols, and updated coordination tools on existing Capsules — including
  standing leads — without recreating them or losing active conversation
  context.
- Docker Desktop restarts and Kubernetes Pod reschedules (ADR 0044) also stop
  wiping harness session history on new Capsules.
- Pre-existing Capsules created before this change incur one fresh harness
  session on their first upgrade (Docker Capsules gain a persistent `home`
  volume on that upgrade; pre-existing Kubernetes Sandboxes stay on `EmptyDir`
  because `volumeClaimTemplates` is immutable).
- Persisting `/home/capsule` expands the persistence surface for Capsule-written
  harness configuration (such as extra hooks or plugins outside Meridian-owned
  setup files) and, unless `agy` supports a config-dir override to
  `/run/meridian-session`, exposes the Antigravity OAuth profile to host disk
  and PVC snapshot backups while active or after an unclean crash. See
  `docs/threat-model.md`.
- Upgrading a Capsule whose Project `setup` depended on a runtime version
  installed inside the pack image does not automatically rerun `setup`; the
  workspace stays untouched so in-progress work is never overwritten.

## Alternatives considered

- **Manual opt-in only, with no persistent home.** Rejected as the target UX:
  wiping `/home/capsule` on every upgrade gives standing leads amnesia on every
  release and forces operators to babysit Capsule upgrades manually.
- **Trigger upgrade synchronously inside a Thread send.** Rejected: on
  Kubernetes a Pod swap and kubelet image pull can take minutes, stalling the
  user's message and turning automatic upgrades into visible latency spikes.
- **Seed the rare fresh-session fallback with a Meridian-generated compaction
  summary.** Rejected: Meridian deliberately does not implement compaction
  semantics (`docs/threat-model.md`), does not invoke LLMs from `meridiand`
  over encrypted transcripts, and avoids re-injecting untrusted transcript text
  into a fresh session's prompt. Standing leads already persist durable working
  and project notes (ADR 0060), which are re-delivered automatically on
  `KindStart`.
- **Migrate `/home/capsule` by tar-copying out of the old container.**
  Rejected: slower, brittle if the container is already stopped/crashed, adds a
  new host-side archive extraction path over untrusted Capsule bytes, and still
  leaves ADR 0044 restarts wiping sessions.
- **Rerun Project `setup` automatically after upgrading.** Rejected: running
  arbitrary setup scripts on top of a dirty `/workspace` risks clobbering
  uncommitted code or failing on modified lockfiles.
- **Delete and recreate the Kubernetes `Sandbox` object.** Rejected: deleting
  the `Sandbox` garbage-collects its owner-referenced `workspace` PVC and
  destroys the workspace. Using `OperatingMode: Suspended → Running` replaces
  only the Pod while preserving the `Sandbox` and its PVCs under existing RBAC.
