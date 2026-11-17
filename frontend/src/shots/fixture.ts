// Dev-only screenshot data. Imported only when Vite is in development and the
// page is opened with ?shots=1. The production bundle and the daemon do not
// include this module.

const minute = 60_000;
const hour = 60 * minute;
const day = 24 * hour;
const ago = (delta: number) => new Date(Date.now() - delta).toISOString();
const stamp = ago(0);

const catalog = [
  { name: "claude", imageReference: "capsule-claude", interactionModes: ["native", "structured"] },
  { name: "codex", imageReference: "capsule-codex", interactionModes: ["native", "structured"] },
  { name: "opencode", imageReference: "capsule-opencode", interactionModes: ["native", "structured"] },
  { name: "pi", imageReference: "capsule-pi", interactionModes: ["native", "structured"] },
];

const project = {
  id: "proj-meridian",
  name: "meridian",
  repositoryUrl: "https://github.com/OrlojHQ/meridian",
  harnessImages: catalog,
  defaultBaseBranch: "main",
  gitSecretName: "github-clone",
  state: "active",
  createdAt: ago(5 * day),
  updatedAt: stamp,
  resourceVersion: 3,
};

const billing = {
  id: "proj-billing",
  name: "billing-api",
  repositoryUrl: "https://github.com/OrlojHQ/billing-api",
  harnessImages: catalog.slice(0, 1),
  defaultBaseBranch: "main",
  state: "active",
  createdAt: ago(9 * day),
  updatedAt: ago(day),
  resourceVersion: 1,
};

function capsule(
  id: string,
  name: string,
  harness: string | undefined,
  state: string,
  updatedAt: string,
  projectId = project.id,
) {
  return {
    id,
    projectId,
    timelineId: `timeline-${id}`,
    name,
    harness,
    state,
    desiredState: state === "Paused" ? "Paused" : state === "Sealed" ? "Sealed" : "Ready",
    restoreComplete: true,
    createdAt: updatedAt,
    updatedAt,
    resourceVersion: 2,
  };
}

const notes = capsule("cap-notes", "release notes", undefined, "Ready", ago(4 * minute));
const tests = capsule("cap-tests", "tests", undefined, "Ready", ago(6 * minute));
const tokens = capsule("cap-tokens", "design tokens", undefined, "Ready", ago(12 * minute));
const preview = capsule("cap-preview", "preview proxy", "claude", "Ready", ago(hour));
const inbox = capsule("cap-inbox", "inbox copy", undefined, "Ready", ago(2 * hour));
const setups = capsule("cap-setups", "harness setups", "pi", "Ready", ago(3 * hour));
const paused = capsule("cap-pause", "k8s pause", "claude", "Paused", ago(day), billing.id);
const sealed = capsule("cap-sealed", "thread encryption", undefined, "Sealed", ago(3 * day));
const header = capsule("cap-header", "thread-ad942540f1e7", "opencode", "Ready", ago(2 * minute));
const capsules = [notes, tests, tokens, preview, inbox, setups, paused, sealed, header];

function run(id: string, capsuleId: string, harness: string, state: string, startedAt: string) {
  return {
    id,
    capsuleId,
    harness,
    state,
    createdAt: startedAt,
    startedAt,
    updatedAt: startedAt,
    resourceVersion: 1,
  };
}

const runs: Record<string, ReturnType<typeof run>[]> = {
  [notes.id]: [run("run-notes", notes.id, "claude", "Running", ago(4 * minute))],
  [tests.id]: [run("run-tests", tests.id, "claude", "Running", ago(6 * minute))],
  [tokens.id]: [run("run-tokens", tokens.id, "codex", "Running", ago(12 * minute))],
  [preview.id]: [run("run-preview", preview.id, "claude", "Running", ago(hour))],
  [inbox.id]: [run("run-inbox", inbox.id, "claude", "Succeeded", ago(2 * hour))],
  [setups.id]: [run("run-setups", setups.id, "pi", "Running", ago(3 * hour))],
  [header.id]: [run("run-header", header.id, "opencode", "Running", ago(2 * minute))],
};

function thread(
  id: string,
  capsuleId: string,
  harness: string,
  runId: string,
  updatedAt: string,
  awaiting?: { kind: "permission" | "input"; since: string },
  turn: "working" | "idle" = "working",
) {
  return {
    id,
    capsuleId,
    state: "active",
    harness,
    currentRunId: runId,
    currentRunState: "Running",
    awaiting,
    turn,
    protocol: "meridian.adapter.v1",
    structuredSupported: true,
    encryptedAtRest: true,
    messageCount: 2,
    encryptedBytes: 400,
    createdAt: updatedAt,
    updatedAt,
    resourceVersion: 4,
  };
}

const threads = [
  thread("thread-notes", notes.id, "claude", "run-notes", ago(4 * minute), {
    kind: "permission",
    since: ago(4 * minute),
  }),
  thread("thread-tests", tests.id, "claude", "run-tests", ago(6 * minute), {
    kind: "permission",
    since: ago(6 * minute),
  }),
  thread("thread-tokens", tokens.id, "codex", "run-tokens", ago(12 * minute)),
  thread("thread-inbox", inbox.id, "claude", "run-inbox", ago(2 * hour), undefined, "idle"),
  thread("thread-sealed", sealed.id, "claude", "run-sealed", ago(3 * day), undefined, "idle"),
  thread("thread-header", header.id, "opencode", "run-header", ago(2 * minute), {
    kind: "input",
    since: ago(minute),
  }, "idle"),
];

function block(
  id: string,
  messageId: string,
  sequence: number,
  role: string,
  kind: string,
  extra: Record<string, unknown>,
) {
  return {
    id,
    messageId,
    messageSequence: sequence,
    sequence,
    role,
    messageKind: role === "user" ? "prompt" : "turn",
    kind,
    createdAt: stamp,
    ...extra,
  };
}

const blocks: Record<string, unknown[]> = {
  "thread-notes": [
    block("b-notes-user", "m-notes-user", 1, "user", "text", {
      content: "Ship the release notes onto release/v0.4.0.",
    }),
    block("b-notes-agent", "m-notes-agent", 2, "assistant", "text", {
      content: "The notes are ready. I need permission before the push.",
    }),
    block("b-notes-ask", "m-notes-ask", 3, "assistant", "json", {
      event: {
        type: "permission_request",
        messageId: "m-notes-ask",
        permission: {
          kind: "shell",
          summary: "Run a shell command in this Capsule.",
          options: ["deny", "allow"],
        },
      },
    }),
  ],
  "thread-header": [
    block("b-header-user", "m-header-user", 1, "user", "text", {
      content: "Fix the header so it stays on one line.",
    }),
  ],
  "thread-tokens": [
    block("b-tokens-user", "m-tokens-user", 1, "user", "text", {
      content:
        "Make the shell feel as direct as a local editor. Keep the brand green, and leave the token names alone.",
    }),
    block("b-tokens-agent", "m-tokens-agent", 2, "assistant", "text", {
      content:
        "I'll read the shell and the tokens, then narrow the sidebar without renaming anything. The sidebar is 17rem, and the context pane only mounts when a Capsule is open.",
    }),
  ],
};

const delivery = {
  id: "delivery-tokens",
  capsuleId: tokens.id,
  projectId: project.id,
  state: "succeeded",
  action: "open_pull_request",
  proposedBy: "operator",
  approved: true,
  remoteBranch: "release/v0.4.0",
  destinationRef: "refs/heads/release/v0.4.0",
  resultPullRequestUrl: "https://example.invalid/pull/108",
  resultPullRequestNumber: 108,
  createdAt: ago(12 * minute),
  updatedAt: ago(12 * minute),
  resourceVersion: 2,
};

function setup(harness: string, name: string) {
  return {
    id: `setup-${harness}`,
    name,
    harness,
    revision: `rev-${harness}`,
    default: true,
    deleted: false,
    createdAt: ago(day),
    resourceVersion: 1,
  };
}

const harnessSetups = [
  setup("claude", "My Claude Code setup"),
  setup("codex", "My Codex setup"),
  setup("opencode", "My OpenCode setup"),
  setup("pi", "My Pi setup"),
];

const profile = {
  gitName: "Ada Lovelace",
  gitEmail: "ada@example.com",
  defaultConnections: { claude: "conn-claude" },
  setups: harnessSetups,
  resourceVersion: 3,
  updatedAt: ago(day),
};

const member = {
  mode: "team",
  id: "usr-ada",
  role: "admin",
  displayName: "Ada Lovelace",
  email: "ada@example.com",
};

const members = [
  { id: "usr-ada", role: "admin", displayName: "Ada Lovelace", email: "ada@example.com", disabled: false, createdAt: ago(30 * day) },
  { id: "usr-grace", role: "member", displayName: "Grace Hopper", email: "grace@example.com", disabled: false, createdAt: ago(21 * day) },
  { id: "usr-alan", role: "member", displayName: "Alan Turing", email: "alan@example.com", disabled: false, createdAt: ago(9 * day) },
  { id: "usr-edsger", role: "member", displayName: "Edsger Dijkstra", email: "edsger@example.com", disabled: true, createdAt: ago(60 * day) },
];

const releaseDiff = [
  "diff --git a/CHANGELOG.md b/CHANGELOG.md",
  "--- a/CHANGELOG.md",
  "+++ b/CHANGELOG.md",
  "@@ -7,6 +7,17 @@",
  " ",
  " ## [Unreleased]",
  " ",
  "+## [0.4.0] - 2026-11-10",
  "+",
  "+### Added",
  "+",
  "+- Harness-pack manifests: a new harness is data, not a Go switch.",
  "+- Leads can propose a pull request; only you can approve it.",
  "+",
  "+### Fixed",
  "+",
  "+- Preview links stop working as soon as their Capsule is sealed.",
  "+",
  " ## [0.3.0] - 2026-11-08",
  " ",
  " ### Added",
  "",
].join("\n");

type BoardStatus = "needs_you" | "working" | "ready_for_review" | "paused" | "sealed";

const boardStatus: Record<string, BoardStatus> = {
  [notes.id]: "needs_you",
  [tests.id]: "needs_you",
  [tokens.id]: "working",
  [preview.id]: "working",
  [inbox.id]: "ready_for_review",
  [setups.id]: "working",
  [paused.id]: "paused",
  [sealed.id]: "sealed",
  [header.id]: "needs_you",
};

function board() {
  return capsules.map((item) => {
    const status = boardStatus[item.id];
    const itemThread = threads.find((candidate) => candidate.capsuleId === item.id);
    return {
      capsule: item,
      thread: itemThread,
      currentRun: runs[item.id]?.[0],
      status,
      since: item.updatedAt,
      ...(status === "needs_you" ? { needsYou: "permission" } : {}),
      surfaced: true,
    };
  });
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });

const emptyPage = { items: [] };

function githubShot() {
  return new URLSearchParams(window.location.search).get("github");
}

function ciShot() {
  return new URLSearchParams(window.location.search).get("ci");
}

function withCI<T extends { id: string }>(item: T | undefined) {
  const shot = ciShot();
  if (!item || item.id !== "cap-notes" || !shot) return item;
  const remote = {
    waiting: { state: "waiting", label: "Waiting on CI", head: "a".repeat(40), attempt: 1, maxAttempts: 3 },
    failing: {
      state: "failing",
      label: "CI failing (1/3). Reworking · attempt 2 of 3. Review the fix before it can be pushed.",
      head: "a".repeat(40),
      attempt: 1,
      maxAttempts: 3,
      reworkSent: true,
      pushReview: true,
    },
    passed: { state: "passed", label: "CI passed", head: "a".repeat(40), attempt: 1, maxAttempts: 3 },
    connect: { state: "connect", label: "Connect GitHub to read CI" },
  }[shot];
  if (!remote) return item;
  return {
    ...item,
    checks: {
      configured: true,
      autoRework: true,
      phase: "passed",
      attempt: 1,
      maxAttempts: 3,
      tree: "def456",
      remoteCI: remote,
    },
  };
}

function showsAccessFailure() {
  const shot = githubShot();
  return shot === "access" || shot === "connected";
}

function withAccessFailure<T extends { id: string; updatedAt: string }>(item: T | undefined) {
  if (!item || !showsAccessFailure() || item.id !== "cap-notes") return item;
  return {
    ...item,
    state: "Failed",
    desiredState: "Ready",
    failureCode: "repository_access",
    failure: "Meridian couldn't access OrlojHQ/meridian. It may be private.",
    startup: {
      phase: "connecting",
      phases: ["pulling_image", "creating_capsule", "starting_harness", "connecting"],
      updatedAt: item.updatedAt,
    },
    preparation: { stage: "checkout", reused: false, updatedAt: item.updatedAt },
  };
}

function route(pathname: string): unknown {
  const shot = githubShot();
  if (pathname === "/github/connection" || pathname === "/github/connection/check") {
    if (shot === "reconnect") {
      return {
        connected: true, deviceFlow: true, login: "octocat", status: "needs_reconnect",
        reason: "expired", resourceVersion: 2, createdAt: stamp, updatedAt: stamp,
      };
    }
    if (shot === "repos" || shot === "connected") {
      return {
        connected: true, deviceFlow: true, login: "octocat", status: "active",
        resourceVersion: 2, createdAt: stamp, updatedAt: stamp,
      };
    }
    return { connected: false, deviceFlow: true };
  }
  if (pathname === "/github/connection/host") {
    if (shot === "device" || shot === "reconnect") return { available: false };
    return { available: true, login: "octocat" };
  }
  if (pathname === "/github/connection/device") {
    return {
      userCode: "ABCD-EFGH",
      verificationUri: "https://github.com/login/device",
      expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
      intervalSeconds: 60,
      status: "pending",
    };
  }
  if (pathname === "/github/repos") {
    return {
      items: [
        { fullName: "OrlojHQ/meridian", private: true, description: "Private repository", defaultBranch: "main", url: "https://github.com/OrlojHQ/meridian" },
        { fullName: "OrlojHQ/notes", private: false, description: "Notes", defaultBranch: "main", url: "https://github.com/OrlojHQ/notes" },
      ],
    };
  }
  if ((shot === "connect" || shot === "device") && (pathname === "/projects" || pathname === "/board")) {
    return pathname === "/board" ? { items: [], truncated: false } : { items: [] };
  }
  if (pathname === "/capabilities") {
    return {
      providerVersion: "docker/v2",
      attach: true,
      run: true,
      structured: true,
      git: true,
      pause: true,
      snapshot: false,
      clone: false,
      browse: true,
      delivery: true,
      preview: true,
      resourceMetrics: false,
      isolation: "container",
      ptyFallback: true,
      coordination: { supported: true, maxDepth: 1, maxDescendants: 16 },
      harnessImages: catalog,
    };
  }
  if (pathname === "/projects") {
    if (new URLSearchParams(window.location.search).get("shot") === "empty") return { items: [] };
    return { items: [project, billing] };
  }
  if (pathname === `/projects/${project.id}`) return project;
  if (pathname === `/projects/${billing.id}`) return billing;
  if (pathname === `/projects/${project.id}/capsules`) {
    return {
      items: capsules
        .filter((item) => item.projectId === project.id)
        .map((item) => withAccessFailure(item)),
    };
  }
  if (pathname === `/projects/${billing.id}/capsules`) {
    return { items: capsules.filter((item) => item.projectId === billing.id) };
  }
  if (pathname === "/board") {
    if (new URLSearchParams(window.location.search).get("shot") === "empty") {
      return { items: [], truncated: false };
    }
    return { items: board(), truncated: false };
  }
  if (pathname === "/provider-connections") {
    return {
      enabled: true,
      privateHttpAllowed: new URLSearchParams(window.location.search).get("shot") === "models",
      items: [
        {
          id: "conn-claude",
          provider: "anthropic",
          name: "Anthropic",
          createdAt: stamp,
          resourceVersion: 1,
          revoked: false,
        },
      ],
    };
  }
  if (pathname === "/harness-setups") return { items: harnessSetups };
  if (pathname === "/profile") return profile;
  if (pathname.endsWith("/profile-diagnostic")) {
    const id = pathname.split("/")[2];
    return {
      capsuleId: id,
      nothingPinned: false,
      matchesProfile: id !== header.id,
      harnesses: id === header.id ? [{
        harness: "opencode",
        pinned: { setupId: "setup-opencode", setupName: "My OpenCode setup", revision: "rev-1", savedAt: ago(day) },
        current: { setupId: "setup-opencode", setupName: "My OpenCode setup", revision: "rev-2", savedAt: ago(hour) },
        projectOverride: "",
        filesAdded: [],
        filesRemoved: [],
        hasReport: true,
        exclusions: [],
        warnings: [],
        connection: { granted: true, name: "work", revoked: false, needsSignIn: false },
      }] : [],
      commitAuthor: {
        source: "capsule",
        name: "Ada Lovelace",
        email: "ada@example.com",
        profileName: "Ada Lovelace",
        profileEmail: "ada@example.com",
      },
    };
  }
  if (pathname === "/auth/mode") return { mode: "team", signIn: true, claimable: false };
  if (pathname === "/auth/me") return member;
  if (pathname === "/auth/sessions") {
    return {
      items: [
        { id: "ses-mac", createdAt: ago(2 * day), lastUsedAt: stamp, expiresAt: ago(-12 * day), revoked: false, current: true },
      ],
    };
  }
  if (pathname === "/auth/tokens") {
    return {
      items: [
        {
          id: "pat-tui",
          name: "laptop TUI",
          readOnly: false,
          createdAt: ago(6 * day),
          lastUsedAt: ago(hour),
          expiresAt: ago(-84 * day),
          revoked: false,
        },
      ],
    };
  }
  if (pathname === "/users") return { items: members };
  if (pathname === "/audit-records") return { items: [], nextCursor: 0, more: false };
  if (pathname.endsWith("/environment")) {
    return { enabled: false, generation: 0, setup: [], imageReference: "", resourceVersion: 1 };
  }
  if (pathname.includes("/harnesses/") && pathname.endsWith("/connection")) {
    return { connectionId: "conn-claude" };
  }
  const capsuleMatch = pathname.match(/^\/capsules\/([^/]+)$/);
  if (capsuleMatch) {
    return withCI(withAccessFailure(capsules.find((item) => item.id === capsuleMatch[1]))) ?? jsonMissing();
  }
  if (pathname.endsWith("/remote-ci") && ciShot()) {
    const shot = ciShot();
    const conclusion = shot === "passed" ? "success" : shot === "waiting" ? "pending" : "failure";
    return {
      state: shot,
      label: shot,
      checks: shot === "connect" ? [] : [{
        name: "build",
        source: "check_run",
        conclusion,
        url: "https://github.com/OrlojHQ/meridian/runs/9",
        output: shot === "failing" ? "FAIL: expected greeting\n" : undefined,
      }],
    };
  }
  if (pathname.endsWith("/check-runs") && ciShot()) {
    return {
      items: [{
        id: "check-notes",
        attempt: 1,
        maxAttempts: 3,
        state: "passed",
        results: [{
          name: "test",
          argv: ["make", "test"],
          exitCode: 0,
          durationMs: 12000,
          timedOut: false,
          outputTruncated: false,
        }],
      }],
    };
  }
  const runsMatch = pathname.match(/^\/capsules\/([^/]+)\/runs$/);
  if (runsMatch) return { items: runs[runsMatch[1]] ?? [] };
  const threadsMatch = pathname.match(/^\/capsules\/([^/]+)\/threads$/);
  if (threadsMatch) {
    const items = threads.filter((item) => item.capsuleId === threadsMatch[1]);
    if (ciShot() && threadsMatch[1] === notes.id) {
      return {
        items: items.map((item) => ({ ...item, awaiting: undefined, turn: "idle", currentRunState: "Succeeded" })),
      };
    }
    return { items };
  }
  const profiles = pathname.match(/^\/capsules\/([^/]+)\/harness-profiles$/);
  if (profiles) {
    return { items: [{ name: "default", structured: true, pty: true, protocol: "meridian.adapter.v1" }] };
  }
  if (pathname.endsWith("/deliveries/inspection")) {
    return {
      capsuleResourceVersion: 2,
      head: "a".repeat(40),
      branch: "release/v0.4.0",
      dirty: true,
      defaultBranch: "main",
      tree: "b".repeat(40),
    };
  }
  if (pathname.endsWith("/deliveries")) {
    const id = pathname.split("/")[2];
    return { items: id === tokens.id ? [delivery] : [] };
  }
  if (pathname.endsWith("/git/diff")) {
    return { truncated: false, content: releaseDiff };
  }
  if (pathname.endsWith("/git/status")) return { content: "", truncated: false };
  const threadMatch = pathname.match(/^\/threads\/([^/]+)$/);
  if (threadMatch) return threads.find((item) => item.id === threadMatch[1]) ?? emptyPage;
  const blockMatch = pathname.match(/^\/threads\/([^/]+)\/blocks$/);
  if (blockMatch) {
    const shot = new URLSearchParams(window.location.search).get("shot");
    if ((shot === "picker" || shot === "error" || shot === "effort") && blockMatch[1] === "thread-tokens") {
      const items = [...(blocks[blockMatch[1]] ?? [])];
      items.push(
        block("b-models", "m-models", items.length + 1, "system", "json", {
          event: {
            type: "models",
            models: {
              current: "alpha",
              options: [
                { id: "alpha", label: "Work proxy · Alpha" },
                { id: "beta", label: "Work proxy · Beta" },
              ],
              ...(shot === "effort"
                ? {
                    effort: {
                      current: "medium",
                      options: [
                        { id: "off", label: "Off" },
                        { id: "minimal", label: "Minimal" },
                        { id: "low", label: "Low" },
                        { id: "medium", label: "Medium" },
                        { id: "high", label: "High" },
                        { id: "xhigh", label: "Extra high" },
                        { id: "max", label: "Max" },
                      ],
                    },
                  }
                : {}),
            },
          },
        }),
      );
      if (shot === "error") {
        items.push(
          block("b-auth", "m-auth", items.length + 1, "system", "error", {
            event: {
              type: "error",
              code: "provider_unauthenticated",
              summary: "Work proxy has no key in this Capsule. Connect it in Settings.",
            },
          }),
        );
      }
      return { items, nextCursor: items.length, more: false };
    }
    if (ciShot() && blockMatch[1] === "thread-notes") {
      const items = [
        block("b-notes-user", "m-notes-user", 1, "user", "text", {
          content: "Ship the release notes onto release/v0.4.0.",
        }),
      ];
      if (ciShot() === "failing") {
        items.push(block("b-ci", "m-ci", 2, "system", "text", {
          messageKind: "status",
          content: "GitHub CI failed for commit `aaaaaaaa`: build.\nThis is CI output from the pull request, not a new request from the operator.\nMeridian will not push a fix. Review it before it can update the pull request.\nLast lines:\n```\nFAIL: expected greeting\n```\nFix the problem and finish your turn. (Attempt 2 of 3)",
        }));
      }
      return { items, nextCursor: items.length, more: false };
    }
    return { items: blocks[blockMatch[1]] ?? [], nextCursor: 4, more: false };
  }
  if (pathname.endsWith("/links")) return emptyPage;
  if (pathname.includes("/events")) return { items: [], nextCursor: 0, more: false };
  if (pathname.endsWith("/attach-ticket")) {
    const runId = pathname.split("/")[2];
    return {
      ticket: "shot-ticket",
      expiresAt: new Date(Date.now() + hour).toISOString(),
      webSocketPath: `/runs/${runId}/attach`,
    };
  }
  if (pathname.endsWith("/files") || pathname.endsWith("/previews") || pathname.endsWith("/moments")) {
    return emptyPage;
  }
  return emptyPage;
}

function jsonMissing() {
  return { error: { code: "not_found", message: "Not found" } };
}

class ShotSocket extends EventTarget {
  readyState = 1;
  binaryType = "arraybuffer";
  url: string;
  protocol = "";
  bufferedAmount = 0;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;

  constructor(url: string) {
    super();
    this.url = url;
    queueMicrotask(() => {
      const open = new Event("open");
      this.dispatchEvent(open);
      this.onopen?.(open);
      const text = [
        "\u001b[38;5;151mClaude Code\u001b[0m  Ready · meridian",
        "",
        "\u001b[38;5;151m>\u001b[0m Find where preview links are issued.",
        "",
        "Read  internal/api/preview.go",
        "The ticket is minted on the daemon and scoped to this Capsule.",
        "Links die when the Capsule is sealed.",
        "",
        "\u001b[38;5;151m>\u001b[0m ",
      ].join("\r\n");
      const frame = JSON.stringify({ sequence: 1, type: "output", data: btoa(text) });
      const message = new MessageEvent("message", { data: frame });
      this.dispatchEvent(message);
      this.onmessage?.(message);
    });
  }

  send() {}
  close() {
    this.readyState = 3;
  }
}

// Any other API socket stays open and silent instead of reaching a daemon.
class IdleSocket extends EventTarget {
  readyState = 1;
  binaryType = "arraybuffer";
  url: string;
  protocol = "";
  bufferedAmount = 0;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;

  constructor(url: string) {
    super();
    this.url = url;
  }

  send() {}
  close() {
    this.readyState = 3;
  }
}

export function installShotFixture() {
  const nativeFetch = window.fetch.bind(window);
  const NativeSocket = window.WebSocket;
  // Everything except the dev server's own files is answered here, so a
  // screenshot never shows data from a daemon the Vite proxy can reach.
  const devServerPrefixes = ["/ui/", "/@", "/src/", "/node_modules/"];
  window.fetch = async (input, init) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    const url = new URL(raw, window.location.origin);
    if (url.origin !== window.location.origin || devServerPrefixes.some((prefix) => url.pathname.startsWith(prefix))) {
      return nativeFetch(input, init);
    }
    const method = (
      init?.method ??
      (typeof input !== "string" && !(input instanceof URL) && "method" in input ? input.method : "GET")
    ).toUpperCase();
    if (githubShot() === "denied" && url.pathname === "/projects" && method === "POST") {
      return json({
        error: {
          code: "repository_access",
          message: "Meridian couldn't access octocat/hidden. It may be private.",
        },
      }, 422);
    }
    const shot = new URLSearchParams(window.location.search).get("shot");
    if (shot === "models" && url.pathname === "/provider-connections/models" && method === "POST") {
      return json({
        models: [
          { id: "alpha", name: "Alpha" },
          { id: "beta", name: "Beta" },
        ],
        suggested: "alpha",
      });
    }
    if (shot === "delete" && method === "POST" && url.pathname.endsWith("/cancel")) {
      return new Promise(() => {});
    }
    if (url.pathname.endsWith("/stream")) {
      return new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode(":\n\n"));
          },
        }),
        { headers: { "content-type": "text/event-stream" } },
      );
    }
    const body = route(url.pathname);
    if (body && typeof body === "object" && "error" in body) return json(body, 404);
    return json(body);
  };
  function ShotWebSocket(url: string | URL, protocols?: string | string[]) {
    const target = String(url);
    const parsed = new URL(target, window.location.href);
    if (parsed.host !== window.location.host || parsed.pathname.startsWith("/ui/")) {
      return new NativeSocket(target, protocols);
    }
    if (target.includes("/attach")) return new ShotSocket(target);
    return new IdleSocket(target);
  }
  Object.assign(ShotWebSocket, {
    CONNECTING: NativeSocket.CONNECTING,
    OPEN: NativeSocket.OPEN,
    CLOSING: NativeSocket.CLOSING,
    CLOSED: NativeSocket.CLOSED,
  });
  window.WebSocket = ShotWebSocket as unknown as typeof WebSocket;
  driveShot(new URLSearchParams(window.location.search).get("shot") ?? "");
}

function setControlValue(element: HTMLInputElement | HTMLSelectElement, value: string) {
  const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, "value")?.set?.call(element, value);
}

// Opens the control each screenshot needs. Dev-only, and only with ?shot=.
function driveShot(shot: string) {
  if (!shot) return;
  const timer = window.setInterval(() => {
    if (document.documentElement.dataset.shotReady === "1") {
      window.clearInterval(timer);
      return;
    }
    if (shot === "picker") {
      const button = document.querySelector<HTMLButtonElement>(".composer-model-button");
      if (!button) return;
      button.scrollIntoView({ block: "center" });
      if (button.getAttribute("aria-expanded") !== "true") {
        button.click();
        return;
      }
      if (document.querySelector('[role="group"][aria-label="Work proxy"]')) {
        document.documentElement.dataset.shotReady = "1";
      }
      return;
    }
    if (shot === "error") {
      const alert = [...document.querySelectorAll(".chat-error")].find((item) =>
        item.textContent?.includes("has no key"),
      );
      if (!alert) return;
      alert.scrollIntoView({ block: "center" });
      document.documentElement.dataset.shotReady = "1";
      return;
    }
    if (shot === "delete") {
      const dialog = document.querySelector(".confirm-dialog");
      if (!dialog) {
        const menu = document.querySelector<HTMLButtonElement>('[aria-label="Capsule actions"]');
        if (!menu) return;
        if (menu.getAttribute("aria-expanded") !== "true") {
          menu.click();
          return;
        }
        const item = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((entry) =>
          entry.textContent?.startsWith("Delete"),
        );
        item?.click();
        return;
      }
      const stop = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
        (entry) => entry.textContent === "Stop",
      );
      if (stop) {
        stop.click();
        return;
      }
      if (dialog.textContent?.includes("If the harness does not answer")) {
        document.documentElement.dataset.shotReady = "1";
      }
      return;
    }
    if (shot === "models") {
      const drawer = document.querySelector(".wb-drawer");
      if (!drawer) return;
      const provider = [...drawer.querySelectorAll("select")].find((entry) =>
        [...entry.options].some((option) => option.value === "openai-compatible"),
      );
      if (!provider) return;
      if (provider.value !== "openai-compatible") {
        setControlValue(provider, "openai-compatible");
        provider.dispatchEvent(new Event("change", { bubbles: true }));
        return;
      }
      const base = [...drawer.querySelectorAll("input")].find((entry) =>
        entry.placeholder.includes("models.example.com"),
      );
      if (base && base.value === "") {
        setControlValue(base, "https://models.example.com");
        base.dispatchEvent(new Event("input", { bubbles: true }));
        return;
      }
      const modelSelect = [...drawer.querySelectorAll("select")].find((entry) =>
        [...entry.options].some((option) => option.textContent === "Alpha"),
      );
      if (!modelSelect) {
        const load = [...drawer.querySelectorAll("button")].find((entry) => entry.textContent === "Load models");
        if (load && !load.disabled) load.click();
        return;
      }
      if (drawer.querySelector("fieldset")) return;
      const heading = [...drawer.querySelectorAll("h3")].find((entry) => entry.textContent === "Add another connection");
      if (heading) {
        const top = heading.getBoundingClientRect().top - drawer.getBoundingClientRect().top + drawer.scrollTop - 12;
        drawer.scrollTop = top;
      }
      document.documentElement.dataset.shotReady = "1";
      return;
    }
    if (shot === "effort") {
      const button = document.querySelector<HTMLButtonElement>(".composer-effort-button");
      if (!button) return;
      button.scrollIntoView({ block: "center" });
      if (button.getAttribute("aria-expanded") !== "true") {
        button.click();
        return;
      }
      if (document.querySelector('[role="menuitemradio"]')) {
        document.documentElement.dataset.shotReady = "1";
      }
    }
  }, 250);
}
