import { HarnessSetupEditor } from "./HarnessSetupEditor";
import { HarnessSetupImport } from "./HarnessSetupImport";
import { connectableHarnesses, connectionSupports, ProviderConnections } from "./ProviderConnections";
import { HarnessMark } from "./ui/HarnessMark";
import { useCallback, useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { HarnessSetup, MutateHarnessSetupRequest, ProviderConnection } from "../api/generated/types.gen";
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
// The drawer shows one harness, or only provider connections when it was
// opened from "Connect a provider". Connections are installation-wide.
type Panel={kind:"harness";harness:string}|{kind:"providers"};

export function HarnessSetups(){
 const location=useLocation();
 const navigate=useNavigate();
 const setups=useQuery({queryKey:["harness-setups"],queryFn:api.harnessSetups});
 const connections=useQuery({queryKey:["provider-connections"],queryFn:api.providerConnections});
 const saved=setups.data?.items.filter(item=>!item.deleted) ?? [];
 const [open,setOpen]=useState<Panel|null>(null);
 const names=[...new Set([...connectableHarnesses, ...saved.map(item=>item.harness)])];
 // React to the link only when its hash changes, so Close stays closed.
 useEffect(()=>{
  const id=location.hash.replace(/^#/, "");
  if(id==="provider-connections") setOpen({kind:"providers"});
  else if(id==="import-setup") setOpen(current=>current??{kind:"harness",harness:connectableHarnesses[0]});
 },[location.hash]);
 const close=useCallback(()=>{
  setOpen(null);
  // Drop the hash so following the same link again reopens the drawer.
  if(location.hash) navigate({pathname:location.pathname,search:location.search},{replace:true});
 },[location.hash, location.pathname, location.search, navigate]);
 useEffect(()=>{
  if(!open) return;
  const onKey=(event:KeyboardEvent)=>{ if(event.key==="Escape") close(); };
  window.addEventListener("keydown", onKey);
  return ()=>window.removeEventListener("keydown", onKey);
 },[open, close]);
 const title=open?.kind==="harness"?harnessName(open.harness):"Connect a provider";
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
    const setup=saved.find(item=>item.harness===harness&&item.default)??saved.find(item=>item.harness===harness);
    const supported=connections.data?.items.filter(item=>!item.revoked && connectionSupports(harness, item)) ?? [];
    const action=setup||supported.length>0?"Manage":"Set up";
    return <li key={harness}>
     <HarnessMark harness={harness} size="lg" />
     <span className="harness-list-copy">
      <strong>{harnessName(harness)}</strong>
      <span className="harness-list-status">
       <span className={setup?"is-ready":undefined}>{setup?`Setup “${setup.name}”`:"No setup imported"}</span>
       <span aria-hidden="true"> · </span>
       <ConnectionStatus items={supported} />
      </span>
     </span>
     <button type="button" className="harness-list-action" aria-label={`${action} ${harnessName(harness)}`} onClick={()=>setOpen({kind:"harness",harness})}>{action}</button>
    </li>;
   })}
  </ul>
  {open && <>
   <button type="button" className="wb-scrim" aria-label="Close settings" onClick={close} />
   <aside className="wb-drawer" role="dialog" aria-label={open.kind==="harness"?`${title} settings`:title}>
    <header>
     <h2>{title}</h2>
     <button type="button" onClick={close}>Close</button>
    </header>
    {open.kind==="harness" && saved.filter(item=>item.harness===open.harness).map(item=><SetupCard key={item.id} setup={item}/>)}
    {open.kind==="harness" && setups.data && <HarnessSetupImport title="Import from this computer" initialHarness={open.harness as Harness} setups={setups.data.items} />}
    <ProviderConnections />
   </aside>
  </>}
 </article>;
}

// One line per harness: what a new Capsule would use, without opening it.
function ConnectionStatus({items}:{items:ProviderConnection[]}){
 const usable=items.filter(item=>!item.needsSignIn);
 if(usable.length===1) return <span className="is-ready">{usable[0].name}</span>;
 if(usable.length>1) return <span className="is-ready">{usable.length} provider connections</span>;
 if(items.length>0) return <span className="is-warning">Sign-in expired</span>;
 return <span>No provider connection</span>;
}
