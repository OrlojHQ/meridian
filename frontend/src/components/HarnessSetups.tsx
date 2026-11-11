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

// Scroll the drawer, not the page, to the import steps and focus the folder
// picker. Re-importing creates a new revision of the default setup.
function showImport(){
 const target=document.getElementById("import-setup");
 const drawer=target?.closest(".wb-drawer");
 if(!target||!drawer) return;
 const header=drawer.querySelector(":scope > header")?.getBoundingClientRect().height ?? 0;
 drawer.scrollTo({top:target.getBoundingClientRect().top-drawer.getBoundingClientRect().top+drawer.scrollTop-header-8,behavior:"smooth"});
 target.querySelector<HTMLButtonElement>("button.primary-button")?.focus({preventScroll:true});
}

function files(count:number){return `${count} ${count===1?"file":"files"}`;}

function SetupCard({setup}:{setup:HarnessSetup}) {
 const cache=useQueryClient();
 const [editing,setEditing]=useState(false);
 const [renaming,setRenaming]=useState(false);
 const [name,setName]=useState(setup.name);
 const [confirmDelete,setConfirmDelete]=useState(false);
 const [expanded,setExpanded]=useState(false);
 const revisions=useQuery({queryKey:["harness-setup-revisions",setup.id,setup.revision],queryFn:()=>api.harnessSetupRevisions(setup.id),enabled:expanded});
 const update=useMutation({mutationFn:(body:Omit<MutateHarnessSetupRequest,"expectedResourceVersion">)=>api.mutateHarnessSetup(setup.id,{...body,expectedResourceVersion:setup.resourceVersion}),onSuccess:()=>{setRenaming(false);return cache.invalidateQueries({queryKey:["harness-setups"]});}});
 const harness=harnessName(setup.harness);
 return <section className="panel setup-card" aria-label={setup.name}>
  <header className="setup-card-header">
   <h3>{setup.name}</h3>
   {setup.default && <span className="badge">Default</span>}
  </header>
  <p className="setup-card-meta">{setup.default?`New ${harness} Capsules start with this setup.`:`Not used by default. New ${harness} Capsules can choose it when you customize them.`} Existing Capsules keep the revision they started with.</p>
  {editing ? <HarnessSetupEditor setup={setup} onClose={()=>setEditing(false)} /> : <>
   <div className="setup-import-actions">
    <button type="button" className="primary-button" onClick={()=>setEditing(true)}>Edit files and skills</button>
    <button type="button" onClick={showImport}>Update from this computer</button>
    <button type="button" aria-expanded={expanded} onClick={()=>setExpanded(!expanded)}>{expanded?"Hide files and history":"Files and history"}</button>
   </div>
   {renaming ? <form className="setup-card-rename" onSubmit={event=>{event.preventDefault();update.mutate({name:name.trim()});}}>
    <label>Setup name<input value={name} maxLength={128} autoFocus disabled={update.isPending} onChange={event=>setName(event.target.value)}/></label>
    <button type="submit" disabled={update.isPending || !name.trim() || name.trim()===setup.name}>Save name</button>
    <button type="button" className="text-button" onClick={()=>{setRenaming(false);setName(setup.name);}}>Cancel</button>
   </form> : <div className="setup-card-more">
    <button type="button" className="text-button" onClick={()=>setRenaming(true)}>Rename</button>
    <button type="button" className="text-button" disabled={update.isPending} onClick={()=>update.mutate({default:!setup.default})}>{setup.default?"Remove default":"Use by default"}</button>
    <button type="button" className="text-button" onClick={()=>setConfirmDelete(true)}>Delete setup</button>
   </div>}
  </>}
  {confirmDelete && <div role="alert" className="setup-card-confirm"><p>Remove this setup from future launches? Existing Capsules retain their revision.</p><button disabled={update.isPending} onClick={()=>update.mutate({deleted:true})}>Remove setup</button> <button className="text-button" onClick={()=>setConfirmDelete(false)}>Cancel</button></div>}
  {update.isError && <p role="alert">{normalizeAPIError(update.error).message}</p>}
  {expanded && !editing && <div className="setup-card-history">
   <h4>Revisions</h4>
   {revisions.isPending && <p role="status">Loading revisions…</p>}
   {revisions.isError && <p role="alert">{normalizeAPIError(revisions.error).message}</p>}
   {revisions.data?.items.map(revision=><details key={revision.id}><summary>{new Date(revision.createdAt).toLocaleString(undefined,{dateStyle:"medium",timeStyle:"short"})} · {files(revision.files.length)}{revision.id===setup.revision?" · Current":""}</summary><ul>{revision.files.map(path=><li key={path}><code>{path}</code></li>)}</ul>{revision.id!==setup.revision && <button disabled={update.isPending} onClick={()=>update.mutate({revision:revision.id})}>Use this revision for new Capsules</button>}</details>)}
  </div>}
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
