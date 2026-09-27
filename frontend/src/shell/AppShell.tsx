import { useQuery } from "@tanstack/react-query";
import {
  useEffect,
  useMemo,
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
import { NewProjectDialog } from "../components/NewProjectDialog";
import { NewWorkspaceDialog } from "../components/NewWorkspaceDialog";
import { HarnessMark } from "../components/ui/HarnessMark";
import {
  ActivityIcon,
  ChevronIcon,
  MenuIcon,
  PlusIcon,
  SearchIcon,
  ThreadIcon,
  WarningIcon,
} from "../components/ui/Icons";
import {
  defaultSession,
  harnessName,
  relativeTime,
  sessionPath,
  type ActivityGroup,
  type CapsuleSession,
} from "./capsuleActivity";
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

export type ShellOutletContext = {
  openNewProject: () => void;
  openLauncher: (project: Project) => void;
};

export function ActivityDot({ group }: { group: ActivityGroup }) {
  return <span className={`state-dot activity-dot-${group}`} aria-hidden="true" />;
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
  const simulated = provider.startsWith("fake/");
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
    <footer className={`status-bar${simulated ? " simulated" : ""}`} aria-label="Status bar">
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
      {simulated && (
        <strong
          className="status-simulated"
          title="meridiand is running with --provider=fake. Capsules are simulated and no agents run."
        >
          <WarningIcon />
          <span>
            Simulated<span className="status-long"> provider</span>
            {" — "}
            <span className="status-long">Capsules are simulated and </span>
            no agents run
          </span>
        </strong>
      )}
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
    </footer>
  );
}

export function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const [filter, setFilter] = useState("");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [contextOpen, setContextOpen] = useState(true);
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const [launchProject, setLaunchProject] = useState<Project>();
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    setLaunchProject(undefined);
  }, [location.key]);

  const { projects, projectItems, items } = useWorkingSet();

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
    if (!query) return items;
    return items.filter(({ capsule, activity }) =>
      [capsule.name, activity.agent, activity.label]
        .some((value) => value.toLowerCase().includes(query)),
    );
  }, [items, filter]);
  const visibleCapsules = useMemo(
    () => visibleItems.map((item) => item.capsule),
    [visibleItems],
  );
  const attentionCount = items.filter(
    (item) => item.activity.group === "attention",
  ).length;

  useEffect(() => {
    setSidebarOpen(false);
    setContextOpen(true);
  }, [location.pathname]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (isTypingTarget(event.target) || event.metaKey || event.ctrlKey || event.altKey) {
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
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [capsuleId, navigate, visibleCapsules]);

  const onSidebarKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (event.key === "Escape") setSidebarOpen(false);
  };

  const grouped = projectItems.map((project) => ({
    project,
    items: visibleItems.filter((item) => item.capsule.projectId === project.id),
  }));
  const hasContext = Boolean(capsuleId);

  return (
    <div
      className={`app-shell ${hasContext && contextOpen ? "has-context" : "no-context"}${
        hasContext ? " with-context-toggle" : ""
      }`}
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
          <Link className="brand" to="/ui/" aria-label="Meridian activity">
            <span className="brand-mark">M</span>
            <span>Meridian</span>
          </Link>
          <button
            type="button"
            className="new-work-button"
            onClick={() => setNewProjectOpen(true)}
          >
            <PlusIcon />
            New project
          </button>
        </header>
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
        <nav className="activity-nav" aria-label="Capsule activity">
          {projects.isPending ? (
            <p className="sidebar-status" role="status">
              Loading activity…
            </p>
          ) : grouped.length === 0 ? (
            <p className="sidebar-status">No Projects</p>
          ) : (
            grouped.map(({ project, items: projectCapsules }) => (
              <section className="project-group" key={project.id}>
                <header>
                  <span>{project.name}</span>
                  <span className="project-group-actions">
                    <small>{projectCapsules.length}</small>
                    <button
                      type="button"
                      className="project-add-button"
                      aria-label={`New Capsule in ${project.name}`}
                      title={`New Capsule in ${project.name}`}
                      onClick={() => setLaunchProject(project)}
                    >
                      <PlusIcon />
                    </button>
                  </span>
                </header>
                {projectCapsules.length === 0 ? (
                  <p className="project-empty">
                    {filter ? "No matches" : "No Capsules"}
                  </p>
                ) : (
                  <ul>
                    {projectCapsules.map(({ capsule, activity, sessions }) => {
                      const selected = capsule.id === capsuleId;
                      const live = sessions.filter((session) => !session.archived);
                      const harness = capsule.harness || live[0]?.harness;
                      return (
                        <li key={capsule.id}>
                          <NavLink
                            to={sessionPath(capsule.id)}
                            className={selected ? "selected" : undefined}
                            title={activity.detail}
                          >
                            <ActivityDot group={activity.group} />
                            <span className="activity-item-copy">
                              <strong>{capsule.name}</strong>
                              <small>
                                {harness && <HarnessMark harness={harness} />}
                                {activity.label}
                              </small>
                            </span>
                            <span className="activity-item-side">
                              <time
                                className="activity-item-time"
                                dateTime={activity.since}
                              >
                                {relativeTime(activity.since, now)}
                              </time>
                              {!selected && live.length > 1 && (
                                <small
                                  className="session-count"
                                  aria-label={`${live.length} sessions`}
                                >
                                  {live.length}
                                </small>
                              )}
                            </span>
                          </NavLink>
                          {selected && live.length > 1 && (
                            <SessionList
                              capsuleId={capsule.id}
                              sessions={live}
                              selected={sessionId || defaultSession(live)?.id || ""}
                              now={now}
                            />
                          )}
                        </li>
                      );
                    })}
                  </ul>
                )}
              </section>
            ))
          )}
        </nav>
        <footer className="sidebar-footer">
          <NavLink to="/ui/settings/harnesses">Harness settings</NavLink>
          <NavLink to="/ui/" end>
            <ActivityIcon />
            Activity
            {attentionCount > 0 && (
              <span
                className="attention-count"
                aria-label={`${attentionCount} need attention`}
              >
                {attentionCount}
              </span>
            )}
          </NavLink>
          <NavLink to="/ui/threads">
            <ThreadIcon />
            Threads
          </NavLink>
        </footer>
      </aside>

      <main id="content" className="workspace-main" tabIndex={-1}>
        <Outlet
          context={
            {
              openNewProject: () => setNewProjectOpen(true),
              openLauncher: setLaunchProject,
            } satisfies ShellOutletContext
          }
        />
      </main>

      {hasContext && (
        <>
          <button
            type="button"
            className={`context-toggle ${contextOpen ? "open" : ""}`}
            aria-label={contextOpen ? "Hide Capsule tools" : "Show Capsule tools"}
            onClick={() => setContextOpen((current) => !current)}
          >
            <ChevronIcon />
          </button>
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
    </div>
  );
}
