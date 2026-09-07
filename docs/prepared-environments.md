# Prepared project environments

Meridian can reuse project dependencies between independent Capsules, including
native harness launches. It first creates the selected approved runtime and checks
out the repository. It then restores a matching prepared workspace or runs the
Project setup command and saves the result. Personal harness configuration and
connections are applied afterward.

In New Capsule, expand **Project environment** to inspect recent preparation, edit
the setup script, disable automatic reuse, or select **Rebuild on next launch**.
Rebuilding invalidates reuse for future launches; it does not modify active
Capsules or immediately start background compute. Project settings are frozen when
preparation begins. Native and structured launchers show preparation progress and
keep the session link pending until a Run has left Queued/Starting.

A failed preparation offers **Retry preparation** on the same Capsule. It preserves
its identity and any pending structured task, and recreates the initial checkout
before rerunning setup. Retry uses the frozen setup command; after changing the
Project script, create a new Capsule to use the updated configuration. Retry never
resets a session that has already completed preparation.

The CLI exposes the same progress through `capsule get` and JSON responses:

```console
meridian capsule get CAPSULE_ID
meridian capsule retry CAPSULE_ID --expected-version VERSION
```

Reuse requires the same Project, exact source commit, actual image digest,
platform, setup arguments, preparation version and Project environment generation.
Repository script contents are covered by the exact commit. Personal instruction
updates do not invalidate the Project workspace cache. Each Project retains at most
eight cache references, with a 72-hour reuse limit. Existing artifact-retention
boundaries apply separately; this is not a hard disk-usage quota.

Missing or invalid artifacts fall back to a fresh checkout and setup. Archives and
manifests are verified before reuse. Images without the staged preparation protocol
use fresh preparation. No provider/harness credentials or private home contents are
included in these workspace artifacts. Keep credentials out of setup scripts and
workspace files; arbitrary source code cannot be proven secret-free by scanning.

This release still checks out source on warm launches, and captures workspace
files rather than whole-machine state. System packages must exist in an approved
image. Setup scripts should be idempotent and finite, without background services.
Command output is bounded and discarded; progress and failure messages are exposed
without persisting raw setup output. Docker integration exercises real cold/warm
reuse. AgentSandbox implements the protocol but needs deployment smoke testing on
an actual cluster before claiming equivalent operational validation.

Private personal-tool caches, resume hooks, background preparation, and connection
renewal are not in this tree.
