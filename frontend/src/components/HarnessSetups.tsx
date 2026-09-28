import { HarnessSetupEditor } from "./HarnessSetupEditor";
import { HarnessSetupImport } from "./HarnessSetupImport";
import { ProviderConnections } from "./ProviderConnections";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { HarnessSetup, MutateHarnessSetupRequest } from "../api/generated/types.gen";
import { harnessName } from "../shell/capsuleActivity";

function SetupCard({setup}:{setup:HarnessSetup}) {
 const cache=useQueryClient();
 const [editing,setEditing]=useState(false);
 const [name,setName]=useState(setup.name);
 const [confirmDelete,setConfirmDelete]=useState(false);
 const [expanded,setExpanded]=useState(false);
 const revisions=useQuery({queryKey:["harness-setup-revisions",setup.id,setup.revision],queryFn:()=>api.harnessSetupRevisions(setup.id),enabled:expanded});
 const update=useMutation({mutationFn:(body:Omit<MutateHarnessSetupRequest,"expectedResourceVersion">)=>api.mutateHarnessSetup(setup.id,{...body,expectedResourceVersion:setup.resourceVersion}),onSuccess:()=>cache.invalidateQueries({queryKey:["harness-setups"]})});
 return <section className="panel">
  <h3>{setup.name} {setup.default && <small>Default</small>}</h3>
  <p>{harnessName(setup.harness)} · Changes apply to new Capsules.</p>
  {!editing && <button onClick={()=>setEditing(true)}>Edit files and skills</button>}
  {editing && <HarnessSetupEditor setup={setup} onClose={()=>setEditing(false)} />}
  <div className="actions"><label>Name<input value={name} maxLength={128} onChange={event=>setName(event.target.value)}/></label>
   <button disabled={update.isPending || !name.trim() || name===setup.name} onClick={()=>update.mutate({name})}>Rename</button>
   <button disabled={update.isPending} onClick={()=>update.mutate({default:!setup.default})}>{setup.default?"Remove default":"Use by default"}</button>
   <button onClick={()=>setExpanded(!expanded)}>{expanded?"Hide files and history":"Files and history"}</button>
   <button onClick={()=>setConfirmDelete(true)}>Delete setup</button>
  </div>
  {confirmDelete && <div role="alert"><p>Remove this setup from future launches? Existing Capsules retain their revision.</p><button disabled={update.isPending} onClick={()=>update.mutate({deleted:true})}>Remove setup</button><button onClick={()=>setConfirmDelete(false)}>Cancel</button></div>}
  {update.isError && <p role="alert">{normalizeAPIError(update.error).message}</p>}
  {expanded && revisions.isPending && <p role="status">Loading revisions…</p>}
  {expanded && revisions.isError && <p role="alert">{normalizeAPIError(revisions.error).message}</p>}
  {expanded && revisions.data?.items.map(revision=><details key={revision.id}><summary>{new Date(revision.createdAt).toLocaleString()} · {revision.files.length} files {revision.id===setup.revision?"· Current":""}</summary><ul>{revision.files.map(path=><li key={path}><code>{path}</code></li>)}</ul>{revision.id!==setup.revision && <button disabled={update.isPending} onClick={()=>update.mutate({revision:revision.id})}>Use this revision for new Capsules</button>}</details>)}
 </section>;
}
export function HarnessSetups(){
 const setups=useQuery({queryKey:["harness-setups"],queryFn:api.harnessSetups});
 const saved=setups.data?.items.filter(item=>!item.deleted) ?? [];
 return <article className="workspace-detail harness-settings">
  <header className="workspace-view-header">
   <div>
    <p className="eyebrow">Settings</p>
    <h1>Harness settings</h1>
    <p className="view-description">Save your agent settings, instructions, skills, and portable tools once. New Capsules start with your default setup.</p>
   </div>
  </header>
  <section className="panel settings-section" aria-labelledby="harness-setups-heading">
   <h2 id="harness-setups-heading">Your setups</h2>
   {setups.isPending && <p role="status">Loading saved setups…</p>}
   {setups.isError && <p role="alert">{normalizeAPIError(setups.error).message}<button onClick={()=>void setups.refetch()}>Retry</button></p>}
   {setups.data && saved.length===0 && <p>No saved setups yet. Import one from this computer below.</p>}
   {saved.map(item=><SetupCard key={item.id} setup={item}/>)}
  </section>
  {setups.data && <HarnessSetupImport title="Import from this computer" setups={setups.data.items} />}
  <ProviderConnections />
 </article>;
}
