import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, normalizeAPIError } from "../api/client";
import type { EnvironmentMutation, ProjectEnvironment as Environment } from "../api/generated/types.gen";

function setupScript(args: string[]) {
  if (args.length >= 3 && ["sh", "/bin/sh", "bash", "/bin/bash"].includes(args[0]!) && args.at(-2) === "-c") return args.at(-1)!;
  return args.map(value => `'${value.replaceAll("'", "'\\''")}'`).join(" ");
}
function Editor({ projectId, environment }: { projectId: string; environment: Environment }) {
  const [script, setScript] = useState(() => setupScript(environment.setup));
  const [enabled, setEnabled] = useState(environment.enabled);
  const client = useQueryClient();
  const mutation = useMutation({
    mutationFn: (input: EnvironmentMutation) => api.updateProjectEnvironment(projectId, input),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["project-environment", projectId] });
      await client.invalidateQueries({ queryKey: ["projects"] });
    },
  });
  function submit(event: FormEvent) {
    event.preventDefault();
    mutation.mutate({ enabled, setup: script.trim() ? ["/bin/sh", "-eu", "-c", script] : [], expectedResourceVersion: environment.resourceVersion });
  }
  return <form onSubmit={submit}>
    <p>Preparation runs inside the Capsule. Saved changes apply to future Capsules.</p>
    <label>Setup script<textarea value={script} onChange={event => setScript(event.target.value)} rows={5} maxLength={4096} placeholder="npm ci" /></label>
    <label><input type="checkbox" checked={enabled} onChange={event => setEnabled(event.target.checked)} /> Reuse matching prepared environments</label>
    {environment.latest && <p>Latest preparation: {environment.latest.stage}{environment.latest.reused ? " (reused)" : ""}.</p>}
    {environment.imageReference && <p>Project base image: <code>{environment.imageReference}</code>. The selected harness pack may use another approved image.</p>}
    <button type="submit" disabled={mutation.isPending}>Save environment</button>{" "}
    <button type="button" disabled={mutation.isPending} onClick={() => mutation.mutate({ rebuild: true, expectedResourceVersion: environment.resourceVersion })}>Rebuild on next launch</button>
    {mutation.isError && <p role="alert">{normalizeAPIError(mutation.error).message}</p>}
  </form>;
}
export function ProjectEnvironment({ projectId }: { projectId: string }) {
  const [open, setOpen] = useState(false);
  const query = useQuery({ queryKey: ["project-environment", projectId], queryFn: ({ signal }) => api.projectEnvironment(projectId, signal), enabled: open });
  return <details onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>Project environment</summary>
    {open && query.isPending && <p role="status">Loading environment…</p>}
    {query.isError && <p role="alert">{normalizeAPIError(query.error).message}</p>}
    {query.data && <Editor key={query.data.resourceVersion} projectId={projectId} environment={query.data} />}
  </details>;
}
