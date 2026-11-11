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
const capsules = [notes, tests, tokens, preview, inbox, setups, paused, sealed];

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

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });

const emptyPage = { items: [] };

function route(pathname: string): unknown {
  if (pathname === "/capabilities") {
    return {
      providerVersion: "docker/v1",
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
      ptyFallback: true,
      harnessImages: catalog,
    };
  }
  if (pathname === "/projects") return { items: [project, billing] };
  if (pathname === `/projects/${project.id}`) return project;
  if (pathname === `/projects/${billing.id}`) return billing;
  if (pathname === `/projects/${project.id}/capsules`) {
    return { items: capsules.filter((item) => item.projectId === project.id) };
  }
  if (pathname === `/projects/${billing.id}/capsules`) {
    return { items: capsules.filter((item) => item.projectId === billing.id) };
  }
  if (pathname === "/board") return { items: [], truncated: false };
  if (pathname === "/provider-connections") {
    return {
      enabled: true,
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
  if (pathname === "/harness-setups") {
    return {
      items: [
        {
          id: "setup-claude",
          name: "Default",
          harness: "claude",
          revision: "rev-1",
          default: true,
          deleted: false,
          createdAt: stamp,
          resourceVersion: 1,
        },
      ],
    };
  }
  if (pathname.endsWith("/environment")) {
    return { enabled: false, generation: 0, setup: [], imageReference: "", resourceVersion: 1 };
  }
  if (pathname.includes("/harnesses/") && pathname.endsWith("/connection")) {
    return { connectionId: "conn-claude" };
  }
  const capsuleMatch = pathname.match(/^\/capsules\/([^/]+)$/);
  if (capsuleMatch) return capsules.find((item) => item.id === capsuleMatch[1]) ?? jsonMissing();
  const runsMatch = pathname.match(/^\/capsules\/([^/]+)\/runs$/);
  if (runsMatch) return { items: runs[runsMatch[1]] ?? [] };
  const threadsMatch = pathname.match(/^\/capsules\/([^/]+)\/threads$/);
  if (threadsMatch) return { items: threads.filter((item) => item.capsuleId === threadsMatch[1]) };
  const profiles = pathname.match(/^\/capsules\/([^/]+)\/harness-profiles$/);
  if (profiles) {
    return { items: [{ name: "default", structured: true, pty: true, protocol: "meridian.adapter.v1" }] };
  }
  if (pathname.endsWith("/deliveries/inspection")) {
    return {
      capsuleResourceVersion: 2,
      head: "abc123",
      branch: "release/v0.4.0",
      dirty: false,
      defaultBranch: "main",
      tree: "def456",
    };
  }
  if (pathname.endsWith("/deliveries")) {
    const id = pathname.split("/")[2];
    return { items: id === tokens.id ? [delivery] : [] };
  }
  if (pathname.endsWith("/git/diff")) {
    return {
      truncated: false,
      content: "diff --git a/frontend/src/styles/shell.css b/frontend/src/styles/shell.css\n@@ -1,4 +1,6 @@\n-old\n+new\n",
    };
  }
  if (pathname.endsWith("/git/status")) return { content: "", truncated: false };
  const threadMatch = pathname.match(/^\/threads\/([^/]+)$/);
  if (threadMatch) return threads.find((item) => item.id === threadMatch[1]) ?? emptyPage;
  const blockMatch = pathname.match(/^\/threads\/([^/]+)\/blocks$/);
  if (blockMatch) return { items: blocks[blockMatch[1]] ?? [], nextCursor: 4, more: false };
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

export function installShotFixture() {
  const nativeFetch = window.fetch.bind(window);
  const NativeSocket = window.WebSocket;
  const apiPrefixes = [
    "/capabilities",
    "/projects",
    "/capsules",
    "/threads",
    "/board",
    "/provider-connections",
    "/harness-setups",
    "/runs",
    "/coordination-requests",
    "/coordination-grants",
    "/thread-links",
    "/secrets",
    "/deliveries",
  ];
  window.fetch = async (input, init) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    const url = new URL(raw, window.location.origin);
    if (!apiPrefixes.some((prefix) => url.pathname.startsWith(prefix))) return nativeFetch(input, init);
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
    if (!target.includes("/attach")) return new NativeSocket(target, protocols);
    return new ShotSocket(target);
  }
  Object.assign(ShotWebSocket, {
    CONNECTING: NativeSocket.CONNECTING,
    OPEN: NativeSocket.OPEN,
    CLOSING: NativeSocket.CLOSING,
    CLOSED: NativeSocket.CLOSED,
  });
  window.WebSocket = ShotWebSocket as unknown as typeof WebSocket;
}
