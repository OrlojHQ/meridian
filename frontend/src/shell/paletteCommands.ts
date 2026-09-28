import type { Capabilities, Project } from "../api/generated/types.gen";
import {
  harnessName,
  sessionPath,
  type ActivityGroup,
} from "./capsuleActivity";
import type { WorkingSetItem } from "./useWorkingSet";

// Commands are built only from the working set the sidebar already holds and
// from the capabilities response. The palette issues no per-Capsule request of
// its own: Git status, files, and previews count as Capsule activity.

export type PaletteGroup =
  | "Recent"
  | "Current Capsule"
  | "Capsules"
  | "Sessions"
  | "Create"
  | "Go to"
  | "Help";

export type PaletteCommand = {
  id: string;
  group: Exclude<PaletteGroup, "Recent">;
  label: string;
  detail?: string;
  keywords?: string;
  harness?: string;
  activity?: ActivityGroup;
  shortcut?: string[];
  danger?: boolean;
  run: () => void;
};

// A Capsule lifecycle request travels to the Capsule workspace in history
// state. The workspace's actions menu owns every lifecycle mutation: Pause and
// Resume run there, while Seal and Delete only open its confirmation.
export type CapsuleActionRequest = "pause" | "resume" | "seal" | "delete";

const capsuleActionRequests: CapsuleActionRequest[] = ["pause", "resume", "seal", "delete"];

export function requestedCapsuleAction(state: unknown): CapsuleActionRequest | undefined {
  if (typeof state !== "object" || state === null) return undefined;
  const action = (state as { capsuleAction?: unknown }).capsuleAction;
  return capsuleActionRequests.find((value) => value === action);
}

export function isApplePlatform() {
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } };
  return /mac|iphone|ipad|ipod/i.test(nav.userAgentData?.platform || nav.platform || "");
}

export const paletteShortcut = () => (isApplePlatform() ? ["⌘", "K"] : ["Ctrl", "K"]);

export type PaletteActions = {
  navigate: (path: string) => void;
  requestCapsuleAction: (capsuleId: string, action: CapsuleActionRequest) => void;
  openNewProject: () => void;
  openLauncher: (project: Project) => void;
  openShortcuts: () => void;
};

export function buildPaletteCommands({
  items,
  projects,
  capsuleId,
  capabilities,
  actions,
}: {
  items: WorkingSetItem[];
  projects: Project[];
  capsuleId: string;
  capabilities?: Capabilities;
  actions: PaletteActions;
}): PaletteCommand[] {
  const commands: PaletteCommand[] = [];
  const projectName = new Map(projects.map((project) => [project.id, project.name]));
  const current = items.find((item) => item.capsule.id === capsuleId)?.capsule;

  if (current) {
    const id = current.id;
    const encoded = encodeURIComponent(id);
    const detail = current.name;
    const push = (command: Omit<PaletteCommand, "group" | "detail">) =>
      commands.push({ ...command, group: "Current Capsule", detail });
    if (capabilities?.git === true) {
      push({
        id: `current:${id}:diff`,
        label: "Open full diff",
        keywords: "changes review git",
        run: () => actions.navigate(`/ui/capsules/${encoded}/diff`),
      });
    }
    push({
      id: `current:${id}:lineage`,
      label: "Open lineage",
      keywords: "timeline moments",
      run: () => actions.navigate(`/ui/timelines/${encodeURIComponent(current.timelineId)}`),
    });
    if (capabilities?.pause === true && current.state === "Ready") {
      push({
        id: `current:${id}:pause`,
        label: "Pause",
        run: () => actions.requestCapsuleAction(id, "pause"),
      });
    }
    if (capabilities?.pause === true && current.state === "Paused") {
      push({
        id: `current:${id}:resume`,
        label: "Resume",
        run: () => actions.requestCapsuleAction(id, "resume"),
      });
    }
    if (capabilities?.snapshot === true && ["Ready", "Paused"].includes(current.state)) {
      push({
        id: `current:${id}:seal`,
        label: "Seal…",
        keywords: "confirm",
        danger: true,
        run: () => actions.requestCapsuleAction(id, "seal"),
      });
    }
    if (!["Sealed", "Deleting", "Deleted"].includes(current.state)) {
      push({
        id: `current:${id}:delete`,
        label: "Delete…",
        keywords: "remove confirm",
        danger: true,
        run: () => actions.requestCapsuleAction(id, "delete"),
      });
    }
  }

  for (const { capsule, activity } of items) {
    commands.push({
      id: `capsule:${capsule.id}`,
      group: "Capsules",
      label: capsule.name,
      detail: [projectName.get(capsule.projectId), activity.label].filter(Boolean).join(" · "),
      keywords: activity.agent,
      harness: capsule.harness || undefined,
      activity: activity.group,
      run: () => actions.navigate(sessionPath(capsule.id)),
    });
  }

  for (const { capsule, sessions } of items) {
    const live = sessions.filter((session) => !session.archived);
    if (live.length < 2) continue;
    for (const session of live) {
      const kind = session.kind === "terminal" ? "Terminal" : session.label;
      commands.push({
        id: `session:${capsule.id}:${session.id}`,
        group: "Sessions",
        label: harnessName(session.harness) || "Session",
        detail: `${kind} · ${capsule.name}`,
        keywords: session.kind,
        harness: session.harness,
        activity: session.group,
        run: () => actions.navigate(sessionPath(capsule.id, session.id)),
      });
    }
  }

  for (const project of projects) {
    commands.push({
      id: `new-capsule:${project.id}`,
      group: "Create",
      label: `New Capsule in ${project.name}`,
      keywords: "launch start agent",
      run: () => actions.openLauncher(project),
    });
  }
  commands.push({
    id: "new-project",
    group: "Create",
    label: "New project",
    shortcut: ["N"],
    run: actions.openNewProject,
  });

  const go = (id: string, label: string, path: string, keywords?: string) =>
    commands.push({
      id: `go:${id}`,
      group: "Go to",
      label,
      keywords,
      run: () => actions.navigate(path),
    });
  go("activity", "Activity", "/ui/", "inbox home");
  go("threads", "Threads", "/ui/threads", "sessions structured");
  go("harnesses", "Harness settings", "/ui/settings/harnesses", "providers setups");

  commands.push({
    id: "help:shortcuts",
    group: "Help",
    label: "Keyboard shortcuts",
    keywords: "keys help",
    shortcut: ["?"],
    run: actions.openShortcuts,
  });
  return commands;
}

const boundary = /[\s\-_/·.:]/;

function tokenScore(token: string, text: string): number | undefined {
  const index = text.indexOf(token);
  if (index >= 0) {
    const wordStart = index === 0 || boundary.test(text[index - 1] ?? "");
    return 100 + (wordStart ? 50 : 0) + token.length * 2 - Math.min(index, 40);
  }
  // An in-order subsequence must stay reasonably dense so that short queries
  // do not match letters scattered across unrelated words.
  const maxSpan = Math.max(token.length * 3, token.length + 6);
  const first = token[0] ?? "";
  let best: number | undefined;
  for (let start = text.indexOf(first); start >= 0; start = text.indexOf(first, start + 1)) {
    let score = start === 0 || boundary.test(text[start - 1] ?? "") ? 4 : 1;
    let last = start;
    for (const character of token.slice(1)) {
      const next = text.indexOf(character, last + 1);
      if (next < 0 || next - start >= maxSpan) {
        score = -1;
        break;
      }
      score += next === last + 1 ? 6 : boundary.test(text[next - 1] ?? "") ? 4 : 1;
      last = next;
    }
    if (score >= 0 && (best === undefined || score > best)) best = score;
  }
  return best;
}

// Every whitespace-separated query token must match the label or, less
// strongly, the detail and keywords, as a substring or in-order subsequence.
export function fuzzyScore(query: string, command: PaletteCommand): number | undefined {
  const tokens = query.toLowerCase().split(/\s+/).filter(Boolean);
  const label = command.label.toLowerCase();
  const all = [label, command.detail, command.keywords, command.group]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  let total = 0;
  for (const token of tokens) {
    const inLabel = tokenScore(token, label);
    const inAll = tokenScore(token, all);
    if (inLabel === undefined && inAll === undefined) return undefined;
    total += Math.max(inLabel === undefined ? 0 : inLabel + 20, inAll ?? 0);
  }
  return total;
}

const groupOrder: PaletteGroup[] = [
  "Current Capsule",
  "Capsules",
  "Sessions",
  "Create",
  "Go to",
  "Help",
];

export type PaletteSection = { group: PaletteGroup; commands: PaletteCommand[] };

export function paletteSections(
  commands: PaletteCommand[],
  query: string,
  recent: string[] = [],
): PaletteSection[] {
  if (!query.trim()) {
    const byId = new Map(commands.map((command) => [command.id, command]));
    const recentCommands = recent.flatMap((id) => byId.get(id) ?? []);
    const shown = new Set(recentCommands.map((command) => command.id));
    const sections: PaletteSection[] = recentCommands.length
      ? [{ group: "Recent", commands: recentCommands }]
      : [];
    for (const group of groupOrder) {
      const inGroup = commands.filter(
        (command) => command.group === group && !shown.has(command.id),
      );
      if (inGroup.length) sections.push({ group, commands: inGroup });
    }
    return sections;
  }
  const scored = commands.flatMap((command) => {
    const score = fuzzyScore(query, command);
    return score === undefined ? [] : [{ command, score }];
  });
  return groupOrder
    .map((group) => {
      const matches = scored
        .filter(({ command }) => command.group === group)
        .sort((a, b) => b.score - a.score);
      return {
        group,
        best: matches[0]?.score ?? 0,
        commands: matches.map(({ command }) => command),
      };
    })
    .filter((section) => section.commands.length > 0)
    .sort((a, b) => b.best - a.best)
    .map(({ group, commands: matched }) => ({ group, commands: matched }));
}

export type ShortcutHelp = { keys: string[][]; description: string };

export const keyboardShortcuts = (): ShortcutHelp[] => [
  { keys: [paletteShortcut()], description: "Open the command palette" },
  { keys: [["N"]], description: "New project" },
  { keys: [["/"]], description: "Filter Capsules" },
  { keys: [["J"], ["↓"]], description: "Next Capsule" },
  { keys: [["K"], ["↑"]], description: "Previous Capsule" },
  { keys: [["?"]], description: "Show keyboard shortcuts" },
];
