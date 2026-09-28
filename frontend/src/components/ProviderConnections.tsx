import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { ProviderConnection, ProviderConnectionRequestWritable } from "../api/generated/types.gen";

export function ProviderConnections(){
 const cache=useQueryClient();const connections=useQuery({queryKey:["provider-connections"],queryFn:api.providerConnections});
 const [provider,setProvider]=useState<"openai"|"anthropic">("openai");const [name,setName]=useState("");const [key,setKey]=useState("");const [editing,setEditing]=useState<ProviderConnection>();
 const save=useMutation({mutationFn:({body,id}:{body:ProviderConnectionRequestWritable,id?:string})=>api.saveProviderConnection(body,id),onSuccess:()=>{setKey("");setEditing(undefined);save.reset();return cache.invalidateQueries({queryKey:["provider-connections"]});}});
 return <section className="panel"><h2>Provider connections</h2><p>Connect your own OpenAI or Anthropic API key once, then authorize it for each Project on its first launch. Subscription logins stay in the harness and are never imported.</p>
 {connections.isPending && <p role="status">Loading connections…</p>}
 {connections.isError && <p role="alert">{normalizeAPIError(connections.error).message}</p>}
 {connections.data && !connections.data.enabled && <p>Reusable connections require an HTTPS Meridian address reachable from Capsules. Ask the installation administrator to enable the provider gateway.</p>}
 {connections.data?.enabled && <form onSubmit={event=>{event.preventDefault();save.mutate({id:editing?.id,body:{name:name||`${provider} API`,provider,apiKey:key,expectedResourceVersion:editing?.resourceVersion??0}});}}>
 <label>Provider<select value={provider} disabled={!!editing} onChange={event=>setProvider(event.target.value as "openai"|"anthropic")}><option value="openai">OpenAI</option><option value="anthropic">Anthropic</option></select></label>
 <label>Connection name<input value={name} maxLength={128} onChange={event=>setName(event.target.value)}/></label>
 <label>API key<input type="password" autoComplete="off" value={key} required maxLength={4096} onChange={event=>setKey(event.target.value)}/></label>
 <button disabled={save.isPending || !key}>{editing?"Replace API key":"Save connection"}</button>{editing && <button type="button" onClick={()=>{setEditing(undefined);setKey("");}}>Cancel</button>}</form>}
 {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
 <ul>{connections.data?.items.map(item=><li key={item.id}>{item.name} · {item.revoked?"Revoked":"Saved"} <button onClick={()=>{setEditing(item);setProvider(item.provider);setName(item.name);setKey("");}}>Reconnect</button> {!item.revoked && <button disabled={save.isPending} onClick={()=>save.mutate({id:item.id,body:{provider:item.provider,name:item.name,apiKey:"",expectedResourceVersion:item.resourceVersion,revoked:true}})}>Revoke</button>}</li>)}</ul>
 </section>;
}
