import { HarnessSetupEditor } from "./HarnessSetupEditor";
import { HarnessSetupImport } from "./HarnessSetupImport";
import { connectableHarnesses, connectionSupports, ProviderConnections } from "./ProviderConnections";
import { useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { HarnessSetup, MutateHarnessSetupRequest } from "../api/generated/types.gen";
import { harnessName } from "../shell/capsuleActivity";
import type { Harness } from "../import/localSetup";

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
 const location=useLocation();
 const setups=useQuery({queryKey:["harness-setups"],queryFn:api.harnessSetups});
 const connections=useQuery({queryKey:["provider-connections"],queryFn:api.providerConnections});
 const saved=setups.data?.items.filter(item=>!item.deleted) ?? [];
 const [open,setOpen]=useState<string|null>(null);
 const names=[...new Set([...connectableHarnesses, ...saved.map(item=>item.harness)])];
 useEffect(()=>{
  const id=location.hash.replace(/^#/, "");
  if(id==="import-setup"||id==="provider-connections") setOpen(current=>current??names[0]??null);
 },[location.hash, names]);
 useEffect(()=>{
  const id=location.hash.replace(/^#/, "");
  if(!id||!open) return;
  document.getElementById(id)?.scrollIntoView({block:"start"});
 },[location.hash, open, setups.data]);
 return <article className="workspace-detail harness-settings">
  <header className="workspace-view-header">
   <div>
    <p className="eyebrow">Settings</p>
    <h1>Harnesses</h1>
    <p className="view-description">New Capsules use the default setup.</p>
   </div>
  </header>
  {setups.isPending && <p role="status">Loading saved setups…</p>}
  {setups.isError && <p role="alert">{normalizeAPIError(setups.error).message}<button onClick={()=>void setups.refetch()}>Retry</button></p>}
  <ul className="harness-list">
   {names.map(harness=>{
    const setup=saved.find(item=>item.harness===harness);
    const linked=connections.data?.items.some(item=>!item.revoked && connectionSupports(harness, item));
    const label=linked?"Connected":setup?"Setup saved":"Not set up";
    const action=linked||setup?"Manage":"Connect";
    return <li key={harness}>
     <span className="harness-list-name"><HarnessMarkPlaceholder harness={harness}/> <strong>{harnessName(harness)}</strong></span>
     <span className="badge">{label}</span>
     <button type="button" onClick={()=>setOpen(harness)}>{action} {harnessName(harness)}</button>
    </li>;
   })}
  </ul>
  {open && <>
   <button type="button" className="wb-scrim" aria-label="Close settings" onClick={()=>setOpen(null)} />
   <aside className="wb-drawer" role="dialog" aria-label={`${harnessName(open)} settings`}>
    <header>
     <h2>{harnessName(open)}</h2>
     <button type="button" onClick={()=>setOpen(null)}>Close</button>
    </header>
    {saved.filter(item=>item.harness===open).map(item=><SetupCard key={item.id} setup={item}/>)}
    {setups.data && <HarnessSetupImport title="Import from this computer" initialHarness={open as Harness} setups={setups.data.items} />}
    <ProviderConnections />
   </aside>
  </>}
 </article>;
}

function HarnessMarkPlaceholder({harness}:{harness:string}){
 return <span className="harness-list-mark" aria-hidden="true">{harnessName(harness).slice(0,1)}</span>;
}
