# ADR 0044: Restart a stopped Capsule runtime instead of failing the Capsule

- Status: Accepted
- Date: 2026-11-12
- Extends: ADRs 0005, 0010, 0019, and 0025

## Context

Docker Desktop and OrbStack stop every container when they quit, restart, or
update, and when the user logs out or the host sleeps long enough. Meridian
creates Capsule containers with no restart policy, so they stay stopped. The
workspace volume, network, image, and supervisor token all survive; only the
processes are gone.

Two things went wrong after that:

- The live refresh (ADR 0025) treated the runtime's "Capsule runtime is not
  active" error as an illegal-transition race and ignored it. A Run whose
  container stopped stayed Working until the daemon restarted, and Stop
  failed with the same error.
- The Docker provider reported a stopped container as an error. When the
  reconciler next looked (startup recovery, or a retry), it retried a few
  times and marked the Capsule Failed. The operator had to start a new
  Capsule even though `docker start` was enough to bring the old one back.

The Kubernetes agent-sandbox provider (ADR 0010) has the same "not active"
path, but there the Sandbox controller and kubelet own the Pod and restart it
on their own.

## Decision

### Stopped is a provider state

- `ports.ProviderStopped` is a runtime that still exists with its workspace
  but is not running. `ProviderResource.Reason` carries a short,
  provider-generated description such as `exit code 137`, never runtime
  output.
- Docker reports `Stopped` for an owned container that has exited or was
  created and never started. A paused container stays `Paused`. A dead,
  removing, or restarting container is still an error.
- Runtime calls on a stopped container return `domain.ErrRuntimeStopped`. It
  wraps `ErrIllegalTransition`, so existing API mapping (409) and callers
  that only check for that are unchanged. A paused runtime keeps the plain
  "not active" error.
- `Resume` on a stopped Docker container starts the same container again and
  waits for its supervisor. It does not recreate the container, rerun setup,
  pull an image, or send a Git credential. The supervisor finds its
  preparation marker in the workspace and reports Ready. If the container
  exits again before the supervisor answers, `Resume` fails at once instead
  of waiting for the timeout.

### The reconciler restarts it, within a budget

- When a Capsule's desired state is Ready and the provider reports
  `Stopped`, the reconciler fails the Runs the stop lost, starts the runtime
  with `Resume`, records a `capsule.runtime_restarted` event, and continues
  as usual, so the Capsule stays Ready.
- Each Capsule gets at most 3 restarts within 10 minutes. A fourth stop in
  that window is a crash loop: the Capsule fails with "Capsule runtime keeps
  stopping (exit code N)". A start that fails also fails the Capsule at once,
  with "Capsule runtime could not be started again". A Failed Capsule is
  handled as before: its workspace is kept until it is deleted, and work
  continues in a new Capsule. The budget is kept in daemon memory, so a
  daemon restart grants a fresh one; that is still bounded for each daemon
  run, and a persisted counter would need a schema change for no real gain.
- When the desired state is Paused, a stopped runtime already meets it: no
  processes run and the workspace is kept. The reconciler leaves it alone
  instead of trying to pause a stopped container. A later resume starts it.
  This is the common desktop case of an idle-paused Capsule whose engine
  restarted.

### Who notices

Meridian restarts a runtime only when the daemon notices it stopped. No
Docker restart policy is set.

- Startup recovery reconciles every non-terminal Capsule, which covers a
  logout or reboot that stopped both the engine and the daemon.
- The live refresh fails an active Run whose runtime stopped with "Capsule
  runtime stopped while this Run was active", pauses its structured Thread
  with its transcript kept, and queues the Capsule for the reconciler.
  Resuming the Thread starts a new session in the restarted Capsule. Nothing
  is resent.
- Starting a Run or a structured session on a stopped runtime fails that
  start with "Capsule runtime was stopped and is being started again; retry
  shortly" and queues the Capsule.

An idle Ready Capsule whose engine restarted while the daemon kept running
is noticed the next time it is used. The refresh does not poll idle
Capsules: that would add an engine call per Capsule per pass for a state
that the first use already finds.

Before it restarts a runtime, the reconciler fails every started,
non-terminal Run of that Capsule, whoever noticed first. The restarted
supervisor would not know them. Queued Runs are left for startup recovery
and explicit retries, as before.

### Agent Sandbox: Kubernetes restarts, Meridian does not

The agent-sandbox provider never reports `Stopped`. The Sandbox controller
recreates the Pod and kubelet restarts its container. Kubernetes applies its
own crash-loop back-off. Meridian has no business starting Pods itself.

- A Sandbox that should be running but whose Pod is not ready returns
  `ErrRuntimeStopped` from runtime calls. Its supervisor and sessions are
  gone, so active Runs fail and Threads pause the same way as with Docker.
- A Ready Capsule whose provider resource reports `Preparing` (the Pod is
  coming back) stays Ready instead of being failed by the re-check. A Sandbox
  that fails readiness for a terminal reason still fails the Capsule.

### Not a restart policy

A Docker restart policy such as `unless-stopped` would bring containers back
without the daemon. We do not set one:

- Containers would run while no control plane watches them: no idle pause,
  no refresh, no lifetime or quota enforcement, and no record of the restart.
- `unless-stopped` also restarts a Capsule that Meridian paused, because an
  engine restart turns a paused container into an exited one. That breaks
  ADR 0019's promise that a paused Capsule runs nothing.
- Docker would restart a crash-looping container forever, where Meridian
  stops after a bounded number of attempts and reports why.

## Consequences

- Restarting an engine no longer strands Capsules or leaves Runs Working.
  The operator sees the Run fail, then resumes the Thread or reruns the
  command in the same Capsule.
- In-flight agent work is lost, as it already was. The workspace is kept.
- A Capsule restart rejoins the operator's existing network and resource
  limits. It adds no Capsule-facing surface and no credential delivery: the
  supervisor token is the one already on the host, and Run secrets are still
  sent per Run.
- Restart counts reset when the daemon restarts, so a Capsule that crashes
  only across daemon restarts can be restarted 3 times per daemon run.
