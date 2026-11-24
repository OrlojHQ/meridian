import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, Navigate, useOutletContext, useParams, useSearchParams } from "react-router-dom";

import { queries } from "../api/queries";
import type { ShellOutletContext } from "../shell/AppShell";
import { Badge } from "./ui/Badge";
import { GitHubAppNotice } from "./GitHubApp";
import { ProjectChecks } from "./ProjectChecks";
import { ProjectEnvironment } from "./ProjectEnvironment";
import { ProjectHarnesses } from "./ProjectHarnesses";
import { ProjectNotes } from "./ProjectNotes";
import { ProjectRoutines } from "./Routines";
import { Tabs } from "./ui/Tabs";

// The server is the authority on isolation floors (ADR 0050); the label
// only surfaces what the Project already requires.
const isolationLabels: Record<string, string> = {
  container: "Requires Container",
  sandbox: "Requires Sandbox",
  microvm: "Requires MicroVM",
};

export function ProjectSettings() {
  const { projectId = "" } = useParams();
  const outlet = useOutletContext<ShellOutletContext | undefined>();
  const projects = useQuery(queries.projects());
  // The Notes tab (ADR 0060) is addressable, so the review page can return
  // to it.
  const [search, setSearch] = useSearchParams();
  const urlTab = search.get("tab") === "notes" ? "notes" : "settings";
  // The router applies a search change as a transition, which kept the old
  // tab selected while the Notes tab rendered. The choice is held here so
  // the tab moves at once; the URL follows, and back and forward still win.
  const [tab, setTab] = useState<"settings" | "notes">(urlTab);
  useEffect(() => setTab(urlTab), [urlTab]);
  if (projects.isPending) return <p role="status">Loading project…</p>;
  if (projects.isError) return <Navigate to="/ui/" replace />;
  const project = projects.data.items.find((item) => item.id === projectId);
  if (!project) return <Navigate to="/ui/" replace />;
  const isolationLabel = project.requiredIsolation
    ? isolationLabels[project.requiredIsolation]
    : undefined;
  return (
    <section className="project-settings">
      <header className="project-settings-header">
        <div>
          <p className="eyebrow">Project</p>
          <h1>
            {project.name}
            {isolationLabel && <Badge>{isolationLabel}</Badge>}
          </h1>
          <p className="launch-hint">Applies to future Capsules.</p>
        </div>
        {outlet?.openNewSession && (project.state ?? "active") === "active" && (
          <button type="button" className="primary-button" onClick={() => outlet.openNewSession(project.id)}>
            New Capsule
          </button>
        )}
      </header>
      <Tabs
        label="Project sections"
        value={tab}
        onChange={(next) => {
          setTab(next === "notes" ? "notes" : "settings");
          setSearch(next === "notes" ? { tab: "notes" } : {}, { replace: true });
        }}
        tabs={[
          { id: "settings", label: "Settings" },
          { id: "notes", label: "Notes" },
        ]}
      />
      {tab === "notes" ? (
        <ProjectNotes key={project.id} project={project} />
      ) : (
        <>
          <dl>
            <div>
              <dt>Repository</dt>
              <dd className="mono">{project.repositoryUrl || "None"}</dd>
            </div>
            {project.defaultBaseBranch && (
              <div>
                <dt>Base branch</dt>
                <dd>{project.defaultBaseBranch}</dd>
              </div>
            )}
            <div>
              <dt>Git clone secret</dt>
              <dd>{project.gitSecretName ? "Set" : "Not set"}</dd>
            </div>
            <div>
              <dt>Git push secret</dt>
              <dd>{project.gitPushSecretName ? "Set" : "Not set"}</dd>
            </div>
          </dl>
          <GitHubAppNotice projectId={project.id} />
          <ProjectHarnesses key={project.id} project={project} />
          <ProjectChecks projectId={project.id} />
          <ProjectRoutines project={project} />
          <ProjectEnvironment projectId={project.id} />
        </>
      )}
      <div className="project-settings-actions">
        <Link className="button-link" to="/ui/">
          Home
        </Link>
        {outlet?.openDeleteProject && (
          <button
            type="button"
            className="danger"
            onClick={() => outlet.openDeleteProject(project)}
          >
            Delete {project.name}
          </button>
        )}
      </div>
    </section>
  );
}
