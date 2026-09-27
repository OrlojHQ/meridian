import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule, Project } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { harnessName } from "../shell/capsuleActivity";

import { HarnessSetupImport } from "./HarnessSetupImport";
import { PreparationProgress } from "./PreparationProgress";

type PendingLaunch = {
  capsule: Capsule;
  harness: string;
};

export function NativeLauncher({
  project,
  harness,
}: {
  project: Project;
  harness: string;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [importing, setImporting] = useState(false);
  const [setup, setSetup] = useState("");
  const [rememberSetup,setRememberSetup]=useState(false);
  const projectSetup=useQuery({queryKey:["project-setup",project.id,harness],queryFn:()=>api.projectHarnessSetup(project.id,harness),enabled:!!harness});
  const [connection,setConnection]=useState<string>();
  const connections=useQuery({queryKey:["provider-connections"],queryFn:api.providerConnections});
  const grant=useQuery({queryKey:["project-connection",project.id,harness],queryFn:()=>api.projectConnection(project.id,harness),enabled:!!harness});
  const selectedConnection=connection ?? grant.data?.connectionId ?? "";
  const availableConnections=connections.data?.items.filter(item=>!item.revoked && (harness!=="claude" || item.provider==="anthropic") && (harness!=="codex" || item.provider==="openai")) ?? [];
  const setups = useQuery({queryKey:["harness-setups"], queryFn:api.harnessSetups});
  const choices = setups.data?.items.filter(item => item.harness === harness && !item.deleted) ?? [];
  const defaultSetup = choices.find(item => item.default);
  const inheritedSetup=projectSetup.data?.setup;
  const shownSetup=choices.find(item=>item.id===(setup || inheritedSetup)) ?? (inheritedSetup === "clean" || setup === "clean" ? undefined : defaultSetup);
  const capsuleName = () => name.trim() || `${harness}-${crypto.randomUUID().slice(0,8)}`;
  const [launch, setLaunch] = useState<PendingLaunch>();

  const capsule = useQuery(queries.capsule(launch?.capsule.id ?? ""));
  const runs = useQuery(queries.runs(launch?.capsule.id ?? ""));
  const mutation = useMutation({
    mutationFn: async () => {
      if(rememberSetup) await api.setProjectHarnessSetup(project.id,harness,setup);
      if(selectedConnection !== (grant.data?.connectionId ?? "")) await api.grantConnection(project.id,harness,selectedConnection);
      return api.createCapsule(project.id, capsuleName(), harness, undefined, setup);
    },
    onSuccess: async (created) => {
      setLaunch({ capsule: created, harness });
      await queryClient.invalidateQueries({
        queryKey: ["capsules", created.projectId],
      });
    },
  });

  const firstRun = runs.data?.items[0];
  useEffect(() => {
    if (launch && firstRun && firstRun.state !== "Queued" && firstRun.state !== "Starting") {
      navigate(`/ui/runs/${encodeURIComponent(firstRun.id)}/terminal`);
    }
  }, [firstRun, launch, navigate]);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (harness) mutation.mutate();
  }

  const busy = mutation.isPending || launch !== undefined;
  const currentCapsule = capsule.data ?? launch?.capsule;
  const launchError =
    currentCapsule?.state === "Failed"
      ? currentCapsule.failure || "Capsule provisioning failed"
      : undefined;

  if (importing && ["opencode", "codex", "claude", "pi"].includes(harness)) return <HarnessSetupImport initialHarness={harness as "opencode" | "codex" | "claude" | "pi"} setups={setups.data?.items ?? []} onCancel={() => setImporting(false)} onSaved={saved => {setSetup(saved.id);setImporting(false);}} />;

  return (
    <section className="launcher-pane">
      <p className="muted-copy">
        Start {harnessName(harness)} in a fresh Capsule and open its terminal.
      </p>
      <form className="launcher-form" onSubmit={submit}>
        {setups.isError ? <p role="alert">Saved setups could not be loaded. <button type="button" onClick={() => void setups.refetch()}>Retry</button></p> :
          <p>{shownSetup ? `Using ${shownSetup.name}` : "Using harness defaults"}. Native account login is separate.</p>}
        {!defaultSetup && ["opencode", "codex", "claude", "pi"].includes(harness) && <button type="button" disabled={busy || setups.isPending || setups.isError} onClick={() => setImporting(true)}>Import my setup</button>}
        {(grant.isError || connections.isError || projectSetup.isError) && <p role="alert">Launch settings could not be loaded. <button type="button" onClick={()=>{void grant.refetch();void connections.refetch();void projectSetup.refetch();}}>Retry</button></p>}
        {connections.data?.enabled && <label>Provider connection<select value={selectedConnection} onChange={event=>setConnection(event.target.value)} disabled={busy}>
          <option value="">Sign in inside the harness</option>{availableConnections.map(item=><option key={item.id} value={item.id}>{item.name}</option>)}
        </select></label>}
        {selectedConnection && selectedConnection!==grant.data?.connectionId && <p>Creating this Capsule authorizes this API connection for {project.name} and future {harness} sessions.</p>}
        {connections.data?.enabled && availableConnections.length===0 && <Link to="/ui/settings/harnesses">Connect provider</Link>}
        <details><summary>Customize</summary>
        <label>Personal setup<select value={setup} onChange={event => setSetup(event.target.value)} disabled={busy}>
          <option value="">Use project default</option><option value="clean">Start clean</option>
          {choices.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
        </select></label>
        <label><input type="checkbox" checked={rememberSetup} onChange={event=>setRememberSetup(event.target.checked)}/>Use this selection for future Capsules in this project</label>
        <label htmlFor="launcher-name">
          Capsule name
          <input
            id="launcher-name"
            value={name}
            maxLength={128}
            disabled={busy}
            placeholder={`${harness || "harness"}-task`}
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        </details>
        <button
          type="submit"
          disabled={busy || !harness || setups.isPending || setups.isError || grant.isPending || grant.isError || connections.isPending || connections.isError || projectSetup.isPending || projectSetup.isError}
        >
          {busy ? "Opening…" : `Start ${harnessName(harness)}`}
        </button>
      </form>
      {mutation.isError && (
        <p className="error-state" role="alert">
          {normalizeAPIError(mutation.error).message}
        </p>
      )}
      {launch && currentCapsule && <PreparationProgress capsule={currentCapsule} />}
      {launch && !launchError && currentCapsule?.state === "Ready" && (
        <p role="status">Applying your personal setup and connecting {launch.harness}…</p>
      )}
      {launch && capsule.isError && (
        <p className="error-state" role="alert">
          {normalizeAPIError(capsule.error).message}
        </p>
      )}
      {launch && runs.isError && (
        <p className="error-state" role="alert">
          {normalizeAPIError(runs.error).message}
        </p>
      )}
    </section>
  );
}
