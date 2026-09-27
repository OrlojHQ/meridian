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

import type { Project } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { NewProjectDialog } from "../components/NewProjectDialog";
import { NewWorkspaceDialog } from "../components/NewWorkspaceDialog";
import {
  ActivityIcon,
  ChevronIcon,
  MenuIcon,
  PlusIcon,
  SearchIcon,
  ThreadIcon,
} from "../components/ui/Icons";
import { relativeTime, type ActivityGroup } from "./capsuleActivity";
import { useWorkingSet } from "./useWorkingSet";
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

export type ShellOutletContext = { openNewProject: () => void };

export function ActivityDot({ group }: { group: ActivityGroup }) {
  return <span className={`state-dot activity-dot-${group}`} aria-hidden="true" />;
}

export function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const [online, setOnline] = useState(navigator.onLine);
  const [filter, setFilter] = useState("");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [contextOpen, setContextOpen] = useState(true);
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const [launchProject, setLaunchProject] = useState<Project>();

  useEffect(() => {
    setLaunchProject(undefined);
  }, [location.key]);

  const { projects, projectItems, items } = useWorkingSet();

  const threadId = pathID(location.pathname, "threads");
  const runId = pathID(location.pathname, "runs");
  const momentId = pathID(location.pathname, "moments");
  const timelineId = pathID(location.pathname, "timelines");
  const directCapsuleId = pathID(location.pathname, "capsules");
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
    const update = () => setOnline(navigator.onLine);
    window.addEventListener("online", update);
    window.addEventListener("offline", update);
    return () => {
      window.removeEventListener("online", update);
      window.removeEventListener("offline", update);
    };
  }, []);

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
      className={`app-shell ${hasContext && contextOpen ? "has-context" : "no-context"}`}
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
                    {projectCapsules.map(({ capsule, activity }) => (
                      <li key={capsule.id}>
                        <NavLink
                          to={`/ui/capsules/${encodeURIComponent(capsule.id)}`}
                          className={
                            capsule.id === capsuleId ? "selected" : undefined
                          }
                          title={activity.detail}
                        >
                          <ActivityDot group={activity.group} />
                          <span className="activity-item-copy">
                            <strong>{capsule.name}</strong>
                            <small>{activity.label}</small>
                          </span>
                          <time
                            className="activity-item-time"
                            dateTime={activity.since}
                          >
                            {relativeTime(activity.since)}
                          </time>
                        </NavLink>
                      </li>
                    ))}
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
          <span
            className={`connection ${online ? "online" : "offline"}`}
            role="status"
          >
            <i />
            {online ? "Connected" : "Offline"}
          </span>
        </footer>
      </aside>

      <main id="content" className="workspace-main" tabIndex={-1}>
        <Outlet context={{ openNewProject: () => setNewProjectOpen(true) } satisfies ShellOutletContext} />
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
              capabilities={capabilities.data}
            />
          )}
        </>
      )}

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
