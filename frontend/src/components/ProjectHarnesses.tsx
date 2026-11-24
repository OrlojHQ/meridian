import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import type { HarnessImage, Project, ProjectPage } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { harnessName } from "../shell/capsuleActivity";
import { HarnessMark } from "./ui/HarnessMark";

export function ProjectHarnesses({ project, embedded = false }: { project: Project; embedded?: boolean }) {
  const queryClient = useQueryClient();
  const capabilities = useQuery(queries.capabilities());
  const packs = project.harnessImages ?? [];
  const available = (capabilities.data?.harnessImages ?? []).filter(
    (item) => !packs.some((pack) => pack.name === item.name),
  );
  const apply = useMutation({
    mutationFn: (pack: HarnessImage) => api.applyProjectHarness(project.id, pack),
    onSuccess: async (updated) => {
      queryClient.setQueryData<ProjectPage>(["projects"], (current) =>
        current && {
          ...current,
          items: current.items.map((item) => item.id === updated.id ? updated : item),
        },
      );
      await queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
  });

  const body = (
    <>
      {packs.length > 0 && (
        <div className="agent-options">
          {packs.map((pack) => (
            <span key={pack.name} className="project-harness">
              <HarnessMark harness={pack.name} size="md" />
              {harnessName(pack.name)}
            </span>
          ))}
        </div>
      )}
      <div className="agent-catalog">
        <p>
          {packs.length === 0
            ? `${project.name} has no agents yet. Add one to start working.`
            : `Add another agent to ${project.name}.`}
        </p>
        {capabilities.isPending ? (
          <p role="status">Loading installed agents…</p>
        ) : capabilities.isError ? (
          <div>
            <p className="inline-error" role="alert">
              {normalizeAPIError(capabilities.error).message}
            </p>
            <button type="button" onClick={() => void capabilities.refetch()}>
              Retry loading agents
            </button>
          </div>
        ) : available.length === 0 ? (
          <p className="notice">
            {(capabilities.data?.harnessImages ?? []).length === 0
              ? "This Meridian installation has no agent images. Configure harness packs for meridiand to add more agents."
              : "All installed agents have been added to this Project."}
          </p>
        ) : (
          <ul className="agent-catalog-list">
            {available.map((pack) => (
              <li key={pack.name}>
                <button
                  type="button"
                  disabled={apply.isPending}
                  onClick={() => apply.mutate(pack)}
                >
                  <HarnessMark harness={pack.name} size="md" />
                  {apply.isPending && apply.variables?.name === pack.name
                    ? `Adding ${harnessName(pack.name)}…`
                    : `Add ${harnessName(pack.name)}`}
                </button>
              </li>
            ))}
          </ul>
        )}
        {apply.isError && (
          <p className="inline-error" role="alert">
            {normalizeAPIError(apply.error).message}
          </p>
        )}
      </div>
    </>
  );

  if (embedded) {
    return <div className="agent-picker settings-embedded-harnesses">{body}</div>;
  }

  return (
    <fieldset className="agent-picker">
      <legend>Harness packs</legend>
      {body}
    </fieldset>
  );
}
