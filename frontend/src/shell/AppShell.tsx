import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import {
  Link,
  NavLink,
  Outlet,
  useLocation,
  useNavigate,
} from "react-router-dom";

import { normalizeAPIError } from "../api/client";
import type { Project } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { DeleteProjectDialog } from "../components/DeleteProjectDialog";
import { NewProjectDialog } from "../components/NewProjectDialog";
import { NewWorkspaceDialog } from "../components/NewWorkspaceDialog";
import { ProjectTree } from "./ProjectTree";
import { HarnessMark } from "../components/ui/HarnessMark";
import { ListSkeleton } from "../components/ui/loading";
import {
  ChangesIcon,
  ChevronIcon,
  CommandIcon,
  FilesIcon,
  MenuIcon,
  PlusIcon,
  PreviewIcon,
  SearchIcon,
  SettingsIcon,
  ThemeIcon,
  TerminalIcon,
  ThreadIcon,
  TrashIcon,
} from "../components/ui/Icons";
import { applyTheme, nextTheme, readTheme, rememberTheme, themeLabel, type ThemePreference } from "../components/ui/theme";
import {
  defaultSession,
  destinationAfterProjectDelete,
  harnessName,
  relativeTime,
  rollupSummary,
  sessionPath,
  type ActivityGroup,
  type CapsuleSession,
} from "./capsuleActivity";
import { CommandPalette, KeyboardShortcutsDialog } from "./CommandPalette";
import {
  GO_PREFIX_MS,
  nextNeedingMePath,
} from "./needsYou";
import {
  buildPaletteCommands,
  isApplePlatform,
  paletteShortcut,
  type CapsuleActionRequest,
  type PaletteCommand,
} from "./paletteCommands";
import { useNeedsYouAlerts } from "./useNeedsYouAlerts";
import { useWorkingSet, type WorkingSetItem } from "./useWorkingSet";
import { WorkspaceContext } from "./WorkspaceContext";

const pathID = (pathname: string, resource: string) => {
  const match = pathname.match(new RegExp(`/ui/${resource}/([^/]+)`));
  return match?.[1] ? decodeURIComponent(match[1]) : "";
};

const isTypingTarget = (target: EventTarget | null) => {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.matches("input, textarea, select, button, [contenteditable=true]") ||
    Boolean(target.closest(".terminal-host"))
  );
};

const inTerminal = (target: EventTarget | null) =>
  target instanceof Element && Boolean(target.closest(".terminal-host"));

// ⌘K on Apple platforms and Ctrl+K elsewhere. Ctrl+K is the shell's
// kill-line, so it is never taken from a focused terminal.
const isPaletteShortcut = (event: KeyboardEvent) => {
  if (event.key.toLowerCase() !== "k" || event.altKey || event.shiftKey) return false;
  if (isApplePlatform()) return event.metaKey && !event.ctrlKey;
  return event.ctrlKey && !event.metaKey && !inTerminal(event.target);
};

const MAX_RECENT = 5;

// Matches the phone layout in shell.css, where the Capsule tools pane covers
// the whole workspace. There it starts closed so a Thread opens on its
// conversation.
const PHONE_LAYOUT = "(max-width: 720px)";
const toolsOpenByDefault = () => !window.matchMedia?.(PHONE_LAYOUT).matches;

export type ShellOutletContext = {
  openNewProject: () => void;
  openLauncher: (project: Project) => void;
  openDeleteProject: (project: Project) => void;
  projectId: string;
  setProjectId: (id: string) => void;
};

export function ActivityDot({ group }: { group: ActivityGroup }) {
  return <span className={`state-dot ui-activity-dot activity-dot-${group}`} aria-hidden="true" />;
}

function ThemeControl() {
  const [theme, setTheme] = useState<ThemePreference>(() => readTheme());
  useEffect(() => {
    applyTheme(theme);
  }, [theme]);
  const label = themeLabel(theme);
  return (
    <button
      type="button"
      className="palette-trigger theme-trigger"
      aria-label={`Color theme, ${label}. Switch color theme.`}
      onClick={() => {
        const value = nextTheme(theme);
        setTheme(value);
        rememberTheme(value);
      }}
    >
      <ThemeIcon />
      {label}
    </button>
  );
}

export { sessionPath };

function SessionList({
  capsuleId,
  sessions,
  selected,
  now,
}: {
  capsuleId: string;
  sessions: CapsuleSession[];
  selected: string;
  now: number;
}) {
  return (
    <ul className="session-list" aria-label="Sessions">
      {sessions.map((session) => (
        <li key={session.id}>
          <Link
            to={sessionPath(capsuleId, session.id)}
            className={session.id === selected ? "selected" : undefined}
            aria-current={session.id === selected ? "page" : undefined}
          >
            <ActivityDot group={session.group} />
            <HarnessMark harness={session.harness} />
            <span className="session-list-label">
              {harnessName(session.harness)}
              <small>{session.label}</small>
            </span>
            <time dateTime={session.since}>{relativeTime(session.since, now)}</time>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function useOnline() {
  const [online, setOnline] = useState(navigator.onLine);
  useEffect(() => {
    const update = () => setOnline(navigator.onLine);
    window.addEventListener("online", update);
    window.addEventListener("offline", update);
    return () => {
      window.removeEventListener("online", update);
      window.removeEventListener("offline", update);
    };
  }, []);
  return online;
}

function notificationPermission(): NotificationPermission | "unsupported" {
  return typeof Notification === "undefined" ? "unsupported" : Notification.permission;
}

// Opt-in uses the browser Notification permission only. Nothing is written to
// browser storage: drafts stay in memory, and there is no phone push.
function NotificationOptIn() {
  const [permission, setPermission] = useState(notificationPermission);
  useEffect(() => {
    if (typeof Notification === "undefined") return;
    let cancelled = false;
    let status: PermissionStatus | undefined;
    const sync = () => {
      if (!cancelled) setPermission(Notification.permission);
    };
    navigator.permissions
      ?.query({ name: "notifications" as PermissionName })
      .then((result) => {
        if (cancelled) return;
        status = result;
        sync();
        result.addEventListener("change", sync);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
      status?.removeEventListener("change", sync);
    };
  }, []);
  if (permission === "unsupported") return null;
  if (permission === "granted") {
    return (
      <span
        className="status-notify-state"
        title='Alerts stay content-free: a count and "Meridian needs you". Nothing is sent to a phone.'
      >
        Alerts on
      </span>
    );
  }
  if (permission === "denied") {
    return (
      <span
        className="status-notify-state"
        title="This browser has blocked notifications for Meridian."
      >
        Alerts blocked
      </span>
    );
  }
  return (
    <button
      type="button"
      className="status-notify"
      aria-label="Enable browser notifications"
      title='Ask this browser for notifications. Alerts stay content-free: a count and "Meridian needs you".'
      onClick={() => {
        void Notification.requestPermission().then((result) => setPermission(result));
      }}
    >
      Notify me
    </button>
  );
}

const statusGroups: { group: ActivityGroup; label: string }[] = [
  { group: "attention", label: "Needs attention" },
  { group: "working", label: "Working" },
  { group: "finished", label: "Ready for review" },
];

// The status bar reports daemon reachability from the capabilities query,
// which records no Capsule activity, and counts only the working set the
// sidebar already derives. The API exposes no resource metrics to show.
function StatusBar({ items }: { items: WorkingSetItem[] }) {
  const online = useOnline();
  const capabilities = useQuery({ ...queries.capabilities(), refetchInterval: 15_000 });
  const provider = capabilities.data?.providerVersion ?? "";
  const failure = capabilities.isError ? normalizeAPIError(capabilities.error) : undefined;
  const connection = !online
    ? { state: "offline", label: "Offline", title: "This browser is offline" }
    : failure
      ? failure.status === 401
        ? { state: "offline", label: "Sign-in required", title: "The daemon rejected this browser session" }
        : { state: "offline", label: "Daemon unreachable", title: failure.message }
      : capabilities.isPending
        ? { state: "pending", label: "Connecting…", title: "Contacting the daemon" }
        : { state: "online", label: "Connected", title: "The daemon API is reachable" };
  return (
    <footer className="status-bar" aria-label="Status bar">
      <span
        className={`status-connection ${connection.state}`}
        role="status"
        title={connection.title}
      >
        <i aria-hidden="true" />
        <span className="status-connection-label">{connection.label}</span>
      </span>
      {provider && (
        <span className="status-provider" title="Capsule provider">
          <span className="sr-only">Provider </span>
          {provider}
        </span>
      )}
      <div className="status-trailing">
        <NotificationOptIn />
        <nav className="status-counts" aria-label="Capsule activity summary">
          {statusGroups.map(({ group, label }) => {
            const count = items.filter((item) => item.activity.group === group).length;
            return (
              <Link
                key={group}
                to="/ui/"
                className={count === 0 ? "empty" : undefined}
                aria-label={`${label}: ${count}`}
                title={label}
              >
                <ActivityDot group={group} />
                {count}
                <span className="status-long" aria-hidden="true">
                  {label.toLowerCase()}
                </span>
              </Link>
            );
          })}
        </nav>
      </div>
    </footer>
  );
}

export function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const [filter, setFilter] = useState("");
  // The Project the home composer starts with. A Project row's + sets it.
  const [composerProject, setComposerProject] = useState("");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [contextOpen, setContextOpen] = useState(toolsOpenByDefault);
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const [deleteProject, setDeleteProject] = useState<Project>();
  const queryClient = useQueryClient();
  const [launchProject, setLaunchProject] = useState<Project>();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
  // Recent palette commands live only in page memory.
  const [recentCommands, setRecentCommands] = useState<string[]>([]);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    setLaunchProject(undefined);
  }, [location.key]);

  const { projects, projectItems, items, ready } = useWorkingSet();

  const threadId = pathID(location.pathname, "threads");
  const runId = pathID(location.pathname, "runs");
  const momentId = pathID(location.pathname, "moments");
  const timelineId = pathID(location.pathname, "timelines");
  const directCapsuleId = pathID(location.pathname, "capsules");
  const sessionId = new URLSearchParams(location.search).get("session") ?? "";
  const thread = useQuery(queries.thread(threadId));
  const run = useQuery(queries.run(runId));
  const moment = useQuery(queries.moment(momentId));
  const timeline = useQuery(queries.timeline(timelineId));
  const capabilities = useQuery(queries.capabilities());
  const capsuleId =
    directCapsuleId ||
    thread.data?.capsuleId ||
    run.data?.capsuleId ||
    moment.data?.capsuleId ||
    timeline.data?.timeline.capsuleId ||
    "";

  const visibleItems = useMemo(() => {
    const query = filter.trim().toLowerCase();
    return items.filter(({ capsule, activity }) => {
      if (!query) return true;
      return [capsule.name, activity.agent, activity.label].some((value) =>
        value.toLowerCase().includes(query),
      );
    });
  }, [items, filter]);
  const visibleCapsules = useMemo(
    () => visibleItems.map((item) => item.capsule),
    [visibleItems],
  );
  const attentionCount = items.filter(
    (item) => item.activity.group === "attention",
  ).length;
  useNeedsYouAlerts({ items, viewedCapsuleId: capsuleId, ready });
  const goPrefix = useRef(false);
  const goPrefixTimer = useRef(0);

  const cliSession = useMemo(() => {
    if (!location.pathname.includes("/capsules/")) return false;
    const current = items.find((item) => item.capsule.id === capsuleId);
    if (!current) return false;
    const selected =
      current.sessions.find((session) => session.id === sessionId) ??
      defaultSession(current.sessions);
    return selected?.kind === "terminal";
  }, [items, capsuleId, sessionId, location.pathname]);
  const toolsDecision = useRef("");

  // Navigating closes the phone sidebar. This must not depend on the working
  // set, which is rebuilt on every render and would close it as it opens.
  useEffect(() => {
    setSidebarOpen(false);
  }, [location.pathname, sessionId]);

  useEffect(() => {
    const key = `${location.pathname}?${sessionId}`;
    if (toolsDecision.current === key) return;
    if (capsuleId && !items.some((item) => item.capsule.id === capsuleId)) return;
    toolsDecision.current = key;
    setContextOpen(cliSession ? false : toolsOpenByDefault());
  }, [location.pathname, sessionId, capsuleId, items, cliSession]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (isTypingTarget(event.target) || event.metaKey || event.ctrlKey || event.altKey) {
        goPrefix.current = false;
        window.clearTimeout(goPrefixTimer.current);
        return;
      }
      const key = event.key.toLowerCase();
      if (goPrefix.current) {
        goPrefix.current = false;
        window.clearTimeout(goPrefixTimer.current);
        if (key === "n") {
          event.preventDefault();
          const path = nextNeedingMePath(items, capsuleId);
          if (path) navigate(path);
          return;
        }
      }
      if (key === "g") {
        goPrefix.current = true;
        window.clearTimeout(goPrefixTimer.current);
        goPrefixTimer.current = window.setTimeout(() => {
          goPrefix.current = false;
        }, GO_PREFIX_MS);
        return;
      }
      if (event.key === "?") {
        event.preventDefault();
        setShortcutsOpen(true);
        return;
      }
      if (event.key === "n") {
        event.preventDefault();
        setNewProjectOpen(true);
        return;
      }
      if (event.key === "/") {
        event.preventDefault();
        document.querySelector<HTMLInputElement>("#activity-filter")?.focus();
        return;
      }
      if (!["j", "k", "ArrowDown", "ArrowUp"].includes(event.key)) return;
      if (visibleCapsules.length === 0) return;
      event.preventDefault();
      const current = visibleCapsules.findIndex((capsule) => capsule.id === capsuleId);
      const direction = event.key === "j" || event.key === "ArrowDown" ? 1 : -1;
      const next =
        current < 0
          ? direction > 0
            ? 0
            : visibleCapsules.length - 1
          : (current + direction + visibleCapsules.length) % visibleCapsules.length;
      navigate(`/ui/capsules/${encodeURIComponent(visibleCapsules[next].id)}`);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.clearTimeout(goPrefixTimer.current);
    };
  }, [capsuleId, items, navigate, visibleCapsules]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!isPaletteShortcut(event)) return;
      event.preventDefault();
      event.stopPropagation();
      setShortcutsOpen(false);
      setPaletteOpen((open) => !open);
    };
    // Capture so a focused terminal cannot consume ⌘K first.
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, []);

  const requestCapsuleAction = useCallback(
    (id: string, action: CapsuleActionRequest) => {
      // Already viewing this Capsule, including its diff. Replace so a later
      // delete can drop the page in one step and Back cannot return to it.
      const onThisCapsule = id === directCapsuleId;
      navigate(
        {
          pathname: `/ui/capsules/${encodeURIComponent(id)}`,
          search: onThisCapsule ? location.search : "",
        },
        { replace: onThisCapsule, state: { capsuleAction: action } },
      );
    },
    [directCapsuleId, location.search, navigate],
  );
  const paletteCommands = useMemo(
    () =>
      paletteOpen
        ? buildPaletteCommands({
            items,
            projects: projectItems,
            capsuleId,
            capabilities: capabilities.data,
            actions: {
              navigate,
              requestCapsuleAction,
              openNewProject: () => setNewProjectOpen(true),
              openLauncher: setLaunchProject,
              openDeleteProject: setDeleteProject,
              openShortcuts: () => setShortcutsOpen(true),
            },
          })
        : [],
    [
      paletteOpen,
      items,
      projectItems,
      capsuleId,
      capabilities.data,
      navigate,
      requestCapsuleAction,
    ],
  );
  const runPaletteCommand = (command: PaletteCommand) => {
    setPaletteOpen(false);
    setRecentCommands((current) =>
      [command.id, ...current.filter((id) => id !== command.id)].slice(0, MAX_RECENT),
    );
    command.run();
  };

  const onSidebarKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (event.key === "Escape") setSidebarOpen(false);
  };

  const grouped = projectItems.map((project) => ({
    project,
    items: visibleItems.filter((item) => item.capsule.projectId === project.id),
  }));
  const hasContext = Boolean(capsuleId);
  const showRail = hasContext && !contextOpen;
  const projectName = new Map(projectItems.map((project) => [project.id, project.name]));
  const byTime = (value: WorkingSetItem) => Date.parse(value.activity.since ?? value.capsule.updatedAt);
  const attentionItems = visibleItems
    .filter((item) => item.activity.group === "attention")
    .sort((left, right) => byTime(left) - byTime(right));
  const activeProjectId = items.find((item) => item.capsule.id === capsuleId)?.capsule.projectId;
  const startCapsuleIn = (project: Project) => {
    setComposerProject(project.id);
    setSidebarOpen(false);
    navigate("/ui/", { state: { focusComposer: true } });
  };
  const renderItem = (
    { capsule, activity, sessions, rollup }: WorkingSetItem,
    { showProject = true, subagents = [] }: { showProject?: boolean; subagents?: WorkingSetItem[] } = {},
  ) => {
    const selected = capsule.id === capsuleId;
    const live = sessions.filter((session) => !session.archived);
    const harness = capsule.harness || live[0]?.harness;
    const kind = live.some((session) => session.kind === "thread") ? "thread" : "cli";
    const project = showProject ? projectName.get(capsule.projectId) : undefined;
    const deletable =
      capsule.role !== "sub" && !["Sealed", "Deleting", "Deleted"].includes(capsule.state);
    return (
      <li key={capsule.id} className="capsule-node">
        <NavLink
          to={sessionPath(capsule.id)}
          className={selected ? "capsule-row selected" : "capsule-row"}
          title={activity.detail}
        >
          <ActivityDot group={activity.group} />
          <span className="capsule-row-body">
            <span className="capsule-row-top">
              {kind === "thread" ? <ThreadIcon /> : <TerminalIcon />}
              <span className="sr-only">{kind === "thread" ? "Thread" : "CLI"}</span>
              <strong>{capsule.name}</strong>
              <time dateTime={activity.since}>{relativeTime(activity.since, now)}</time>
            </span>
            <span className="capsule-row-sub">
              {harness && <HarnessMark harness={harness} />}
              <span>
                {project}
                {project ? " · " : ""}
                {activity.label}
              </span>
              {!selected && live.length > 1 && (
                <small className="session-count" aria-label={`${live.length} sessions`}>
                  {live.length}
                </small>
              )}
            </span>
            {rollup && rollup.total > 0 && (
              <small className="activity-item-rollup">Subagents: {rollupSummary(rollup)}</small>
            )}
          </span>
        </NavLink>
        {deletable && (
          <button
            type="button"
            className="icon-button capsule-row-delete"
            aria-label={`Delete ${capsule.name}`}
            title="Delete Capsule"
            onClick={() => {
              setSidebarOpen(false);
              requestCapsuleAction(capsule.id, "delete");
            }}
          >
            <TrashIcon />
          </button>
        )}
        {selected && live.length > 1 && (
          <SessionList
            capsuleId={capsule.id}
            sessions={live}
            selected={sessionId || defaultSession(live)?.id || ""}
            now={now}
          />
        )}
        {subagents.length > 0 && (
          <ul className="sidebar-subagents" aria-label={`Subagents of ${capsule.name}`}>
            {subagents.map((subagent) => renderItem(subagent))}
          </ul>
        )}
      </li>
    );
  };

  return (
    <div
      className={`app-shell workbench ${hasContext && contextOpen ? "has-context" : "no-context"}${
        showRail ? " with-rail" : ""
      }${hasContext && contextOpen ? " with-context-toggle" : ""}${cliSession ? " cli-session" : ""}`}
    >
      <a className="skip-link" href="#content">
        Skip to workspace
      </a>
      <button
        type="button"
        className="mobile-menu-button icon-button"
        aria-label="Open activity sidebar"
        onClick={() => setSidebarOpen(true)}
      >
        <MenuIcon />
      </button>
      {sidebarOpen && (
        <button
          type="button"
          className="sidebar-scrim"
          aria-label="Close activity sidebar"
          onClick={() => setSidebarOpen(false)}
        />
      )}
      <aside
        className={`activity-sidebar ${sidebarOpen ? "open" : ""}`}
        aria-label="Activity"
        onKeyDown={onSidebarKeyDown}
      >
        <header className="sidebar-header">
          <Link className="brand" to="/ui/" aria-label="Meridian home">
            <span className="brand-mark">M</span>
            <span>Meridian</span>
          </Link>
          <button type="button" className="new-work-button" onClick={() => setNewProjectOpen(true)}>
            <PlusIcon />
            New project
          </button>
        </header>
        <button
          type="button"
          className="primary-button new-capsule"
          onClick={() => {
            setSidebarOpen(false);
            navigate("/ui/", { state: { focusComposer: true } });
          }}
        >
          <PlusIcon />
          New Capsule
        </button>
        <label className="activity-filter" htmlFor="activity-filter">
          <SearchIcon />
          <span className="sr-only">Filter Capsules</span>
          <input
            id="activity-filter"
            value={filter}
            placeholder="Filter work"
            onChange={(event) => setFilter(event.target.value)}
          />
          <kbd>/</kbd>
        </label>
        <nav className="sidebar-links" aria-label="Views">
          <NavLink to="/ui/settings/harnesses">
            <SettingsIcon />
            Harness settings
          </NavLink>
        </nav>
        <nav className="activity-nav" aria-label="Capsule activity">
          {projects.isPending ? (
            <ListSkeleton label="Loading activity…" />
          ) : projects.isError ? (
            <p className="sidebar-status" role="alert">
              Projects could not be loaded
            </p>
          ) : grouped.length === 0 ? (
            <nav className="first-run" aria-label="Get started">
              <Link to="/ui/settings/harnesses#import-setup">Import setup</Link>
              <Link to="/ui/settings/harnesses#provider-connections">
                Connect a provider
              </Link>
              <button type="button" onClick={() => setNewProjectOpen(true)}>
                Create a Project
              </button>
            </nav>
          ) : (
            <>
              <section className="capsule-group">
                <h2>
                  Needs you
                  {attentionItems.length > 0 && (
                    <b aria-label={`${attentionItems.length} need attention`}>{attentionItems.length}</b>
                  )}
                </h2>
                {attentionItems.length === 0 ? (
                  <p className="capsule-empty">Nothing waiting</p>
                ) : (
                  <ul>
                    {attentionItems.map((item) => renderItem(item))}
                  </ul>
                )}
              </section>
              <ProjectTree
                projects={projectItems}
                items={visibleItems}
                filter={filter}
                activeProjectId={activeProjectId}
                renderItem={(item) => renderItem(item, { showProject: false })}
                onNewCapsule={startCapsuleIn}
                onDeleteProject={setDeleteProject}
              />
            </>
          )}
        </nav>
        <footer className="sidebar-footer">
          <ThemeControl />
          <button
            type="button"
            className="palette-trigger"
            aria-keyshortcuts={isApplePlatform() ? "Meta+K" : "Control+K"}
            onClick={() => {
              setSidebarOpen(false);
              setPaletteOpen(true);
            }}
          >
            <CommandIcon />
            Commands
            <span className="key-combo" aria-hidden="true">
              {paletteShortcut().map((key) => (
                <kbd key={key}>{key}</kbd>
              ))}
            </span>
          </button>
        </footer>
      </aside>

      <main id="content" className="workspace-main" tabIndex={-1}>
        <Outlet
          context={
            {
              openNewProject: () => setNewProjectOpen(true),
              openLauncher: setLaunchProject,
              openDeleteProject: setDeleteProject,
              projectId: composerProject,
              setProjectId: setComposerProject,
            } satisfies ShellOutletContext
          }
        />
      </main>

      {deleteProject && (
        <DeleteProjectDialog
          project={deleteProject}
          onCancel={() => setDeleteProject(undefined)}
          onDeleted={(deleted) => {
            navigate(
              destinationAfterProjectDelete(
                deleted.id,
                projectItems,
                items.map((item) => item.capsule),
              ),
              { replace: true },
            );
            setDeleteProject(undefined);
            void queryClient.invalidateQueries({ queryKey: ["projects"] });
            void queryClient.invalidateQueries({ queryKey: ["capsules", deleted.id] });
          }}
        />
      )}

      {showRail && (
        <nav className="tool-rail" aria-label="Capsule tools">
          <button type="button" className="tool-rail-expand" aria-label="Show Capsule tools" title="Show Capsule tools" onClick={() => setContextOpen(true)}>
            <ChevronIcon />
          </button>
          <button type="button" onClick={() => setContextOpen(true)}>
            <ChangesIcon />
            Changes
          </button>
          <button type="button" onClick={() => setContextOpen(true)}>
            <FilesIcon />
            Files
          </button>
          <button type="button" onClick={() => setContextOpen(true)}>
            <PreviewIcon />
            Preview
          </button>
        </nav>
      )}
      {hasContext && (
        <>
          {contextOpen && (
          <button
            type="button"
            className="context-toggle open"
            aria-label="Hide Capsule tools"
            onClick={() => setContextOpen(false)}
          >
            <ChevronIcon />
          </button>
          )}
          {contextOpen && (
            <WorkspaceContext
              capsuleId={capsuleId}
              pathname={`${location.pathname}${location.hash}`}
              session={sessionId}
              capabilities={capabilities.data}
            />
          )}
        </>
      )}

      <StatusBar items={items} />

      <NewProjectDialog
        open={newProjectOpen}
        harnessImages={capabilities.data?.harnessImages ?? []}
        onClose={() => setNewProjectOpen(false)}
      />
      <NewWorkspaceDialog
        open={Boolean(launchProject)}
        project={launchProject}
        onClose={() => setLaunchProject(undefined)}
      />
      {paletteOpen && (
        <CommandPalette
          commands={paletteCommands}
          recent={recentCommands}
          onRun={runPaletteCommand}
          onClose={() => setPaletteOpen(false)}
        />
      )}
      {shortcutsOpen && (
        <KeyboardShortcutsDialog onClose={() => setShortcutsOpen(false)} />
      )}
    </div>
  );
}
