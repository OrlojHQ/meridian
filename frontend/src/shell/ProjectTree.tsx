import { useCallback, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";

import type { Project } from "../api/generated/types.gen";
import { ChevronIcon, PlusIcon, SettingsIcon, TrashIcon } from "../components/ui/Icons";
import type { WorkingSetItem } from "./useWorkingSet";

const COLLAPSED_KEY = "meridian.sidebar.collapsedProjects";

// Which Projects are collapsed is a per-browser convenience. Storage can be
// missing or refuse access, so the tree works without it.
function readCollapsed(): Set<string> {
  try {
    const raw = window.localStorage.getItem(COLLAPSED_KEY);
    const value: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : []);
  } catch {
    return new Set();
  }
}

function writeCollapsed(ids: Set<string>) {
  try {
    window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...ids]));
  } catch {
    // Collapsing still works for this page.
  }
}

function useCollapsedProjects() {
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const toggle = useCallback((id: string) => {
    setCollapsed((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      writeCollapsed(next);
      return next;
    });
  }, []);
  return { collapsed, toggle };
}

function activityTime(item: WorkingSetItem) {
  return Date.parse(item.activity.since ?? item.capsule.updatedAt) || 0;
}

/**
 * Projects with their Capsules nested, most recently active first. Hover or
 * focus shows a Project's actions: start a Capsule, settings, and delete,
 * which opens the existing confirmation.
 */
export function ProjectTree({
  projects,
  items,
  filter,
  activeProjectId,
  renderItem,
  onNewCapsule,
  onDeleteProject,
}: {
  projects: Project[];
  /** Capsules already narrowed by the filter text. */
  items: WorkingSetItem[];
  filter: string;
  /** The Project of the open Capsule stays expanded. */
  activeProjectId?: string;
  renderItem: (item: WorkingSetItem) => ReactNode;
  onNewCapsule: (project: Project) => void;
  onDeleteProject: (project: Project) => void;
}) {
  const { collapsed, toggle } = useCollapsedProjects();
  const query = filter.trim().toLowerCase();
  const nodes = projects
    .map((project) => {
      const all = items.filter((item) => item.capsule.projectId === project.id);
      // Capsules that need the operator are listed once, under Needs you.
      const capsules = all
        .filter((item) => item.activity.group !== "attention")
        .sort((left, right) => activityTime(right) - activityTime(left));
      const waiting = all.length - capsules.length;
      const latest = Math.max(
        Date.parse(project.updatedAt) || 0,
        ...all.map(activityTime),
      );
      return { project, capsules, total: all.length, waiting, latest };
    })
    .filter(
      ({ project, total }) =>
        !query || total > 0 || project.name.toLowerCase().includes(query),
    )
    .sort((left, right) => right.latest - left.latest);

  return (
    <section className="capsule-group project-tree" aria-labelledby="project-tree-title">
      <h2 id="project-tree-title">Projects</h2>
      {nodes.length === 0 ? (
        <p className="capsule-empty">No matches</p>
      ) : (
        <ul className="project-tree-list">
          {nodes.map(({ project, capsules, total, waiting }) => {
            const open =
              Boolean(query) || project.id === activeProjectId || !collapsed.has(project.id);
            const listId = `project-capsules-${project.id}`;
            const accepting = (project.state ?? "active") === "active";
            const panel = open && (capsules.length > 0 || (total === 0 && !query && accepting));
            return (
              <li key={project.id} className="project-node">
                <div className="project-row">
                  <button
                    type="button"
                    className="project-toggle"
                    aria-expanded={open}
                    aria-controls={panel ? listId : undefined}
                    onClick={() => toggle(project.id)}
                  >
                    <ChevronIcon className="project-chevron" />
                    <span className="project-name">{project.name}</span>
                    {waiting > 0 && (
                      <b className="project-waiting" title="In Needs you">
                        {waiting}
                        <span className="sr-only"> need you</span>
                      </b>
                    )}
                    <span className="project-count">
                      {total}
                      <span className="sr-only"> {total === 1 ? "Capsule" : "Capsules"}</span>
                    </span>
                  </button>
                  <span className="project-actions">
                    <button
                      type="button"
                      className="icon-button"
                      aria-label={`New Capsule in ${project.name}`}
                      title="New Capsule"
                      disabled={!accepting}
                      onClick={() => onNewCapsule(project)}
                    >
                      <PlusIcon />
                    </button>
                    <Link
                      className="icon-button"
                      to={`/ui/projects/${encodeURIComponent(project.id)}`}
                      aria-label={`${project.name} settings`}
                      title="Project settings"
                    >
                      <SettingsIcon />
                    </Link>
                    <button
                      type="button"
                      className="icon-button project-delete"
                      aria-label={`Delete ${project.name}`}
                      title="Delete project"
                      disabled={project.state === "deleting"}
                      onClick={() => onDeleteProject(project)}
                    >
                      <TrashIcon />
                    </button>
                  </span>
                </div>
                {project.state === "failed" && (
                  <p className="project-delete-failure" role="status">
                    Deletion stopped{project.failure ? `: ${project.failure}` : ""}
                  </p>
                )}
                {project.state === "deleting" && <p className="project-tree-note">Deleting…</p>}
                {open &&
                  (capsules.length > 0 ? (
                    <ul id={listId} className="project-capsules" aria-label={`${project.name} Capsules`}>
                      {capsules.map((item) => renderItem(item))}
                    </ul>
                  ) : (
                    total === 0 &&
                    !query &&
                    accepting && (
                      <p id={listId} className="project-tree-note">
                        No Capsules yet ·{" "}
                        <button type="button" className="text-button" onClick={() => onNewCapsule(project)}>
                          Start one
                        </button>
                      </p>
                    )
                  ))}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
