import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useOutletContext, useParams } from "react-router-dom";

import { queries } from "../api/queries";
import type { ShellOutletContext } from "../shell/AppShell";
import { harnessName } from "../shell/capsuleActivity";
import { ProjectEnvironment } from "./ProjectEnvironment";

export function ProjectSettings() {
  const { projectId = "" } = useParams();
  const outlet = useOutletContext<ShellOutletContext | undefined>();
  const projects = useQuery(queries.projects());
  if (projects.isPending) return <p role="status">Loading project…</p>;
  if (projects.isError) return <Navigate to="/ui/" replace />;
  const project = projects.data.items.find((item) => item.id === projectId);
  if (!project) return <Navigate to="/ui/" replace />;
  const packs = project.harnessImages ?? [];
  return (
    <section className="project-settings">
      <header>
        <p className="eyebrow">Project</p>
        <h1>{project.name}</h1>
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
          <dt>Harness packs</dt>
          <dd>
            {packs.length === 0
              ? "None"
              : packs.map((pack) => harnessName(pack.name)).join(", ")}
          </dd>
        </div>
        <div>
          <dt>Git clone secret</dt>
          <dd>{project.gitSecretName ? "Set" : "Not set"}</dd>
        </div>
        <div>
          <dt>Git push secret</dt>
          <dd>{project.gitPushSecretName ? "Set" : "Not set"}</dd>
        </div>
      </dl>
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
