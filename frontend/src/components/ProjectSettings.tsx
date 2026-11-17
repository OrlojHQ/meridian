import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useOutletContext, useParams } from "react-router-dom";

import { queries } from "../api/queries";
import type { ShellOutletContext } from "../shell/AppShell";
import { Badge } from "./ui/Badge";
import { ProjectChecks } from "./ProjectChecks";
import { ProjectEnvironment } from "./ProjectEnvironment";
import { ProjectHarnesses } from "./ProjectHarnesses";

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
  if (projects.isPending) return <p role="status">Loading project…</p>;
  if (projects.isError) return <Navigate to="/ui/" replace />;
  const project = projects.data.items.find((item) => item.id === projectId);
  if (!project) return <Navigate to="/ui/" replace />;
  const isolationLabel = project.requiredIsolation
    ? isolationLabels[project.requiredIsolation]
    : undefined;
  return (
    <section className="project-settings">
      <header>
        <p className="eyebrow">Project</p>
        <h1>
          {project.name}
          {isolationLabel && <Badge>{isolationLabel}</Badge>}
        </h1>
        <p className="launch-hint">Applies to future Capsules.</p>
      </header>
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
      <ProjectHarnesses key={project.id} project={project} />
      <ProjectChecks projectId={project.id} />
      <ProjectEnvironment projectId={project.id} />
      <div className="project-settings-actions">
        <Link className="button-link" to="/ui/">
          Home
        </Link>
        {outlet?.openLauncher && (
          <button type="button" onClick={() => outlet.openLauncher(project)}>
            Start work in {project.name}
          </button>
        )}
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
