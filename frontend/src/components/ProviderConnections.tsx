import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { ProviderConnection, ProviderConnectionRequestWritable, ProviderConnectionType, ReasoningEffortLevel } from "../api/generated/types.gen";

const providerLabels:Record<ProviderConnectionType,string>={openai:"OpenAI",anthropic:"Anthropic","openai-compatible":"OpenAI-compatible endpoint","anthropic-compatible":"Anthropic-compatible endpoint"};
const isCustom=(provider:ProviderConnectionType)=>provider==="openai-compatible"||provider==="anthropic-compatible";

// Mirrors harnesssetup.ConnectionHarness: capsuled writes provider configuration only for these harnesses.
export const connectableHarnesses=["opencode", "codex", "claude", "pi", "dsh"];

// Mirrors harnesssetup.ConnectionSupported; the server enforces the same pairing.
export function connectionSupports(harness:string,item:ProviderConnection){
 switch(harness){
 case "claude": return item.provider==="anthropic"||item.provider==="anthropic-compatible";
 case "codex": return item.provider==="openai"||item.provider==="openai-compatible";
 case "opencode": case "pi": return item.provider==="openai"||item.provider==="anthropic"||(item.provider==="openai-compatible"&&!!item.defaultModel);
 case "dsh": return isCustom(item.provider)&&!!item.defaultModel;
 default: return !isCustom(item.provider);
 }
}

const effortLevels:ReasoningEffortLevel[]=["minimal","low","medium","high","xhigh","max"];
const effortLabels:Record<ReasoningEffortLevel,string>={minimal:"Minimal",low:"Low",medium:"Medium",high:"High",xhigh:"Extra high",max:"Max"};

export function ProviderConnections(){
 const cache=useQueryClient();const connections=useQuery({queryKey:["provider-connections"],queryFn:api.providerConnections});
 const [provider,setProvider]=useState<ProviderConnectionType>("openai");const [name,setName]=useState("");const [key,setKey]=useState("");const [editing,setEditing]=useState<ProviderConnection>();
 const [baseUrl,setBaseUrl]=useState("");const [model,setModel]=useState("");const [privateHttp,setPrivateHttp]=useState(false);const [efforts,setEfforts]=useState<ReasoningEffortLevel[]>([]);
 const custom=isCustom(provider);const privateHttpAllowed=!!connections.data?.privateHttpAllowed;
 const reset=()=>{setKey("");setEditing(undefined);setBaseUrl("");setModel("");setPrivateHttp(false);setEfforts([]);};
 const save=useMutation({mutationFn:({body,id}:{body:ProviderConnectionRequestWritable,id?:string})=>api.saveProviderConnection(body,id),onSuccess:()=>{reset();save.reset();return cache.invalidateQueries({queryKey:["provider-connections"]});}});
 const body=():ProviderConnectionRequestWritable=>({name:name||(custom?providerLabels[provider]:`${provider} API`),provider,apiKey:key,expectedResourceVersion:editing?.resourceVersion??0,...(custom?{baseUrl,defaultModel:model||undefined,allowPrivateHttp:privateHttp,...(model&&efforts.length>0?{reasoningEfforts:effortLevels.filter(level=>efforts.includes(level))}:{})}:{})});
 return <section className="panel"><h2>Provider connections</h2><p>Connect your own OpenAI or Anthropic API key, or a self-hosted model server such as vLLM, Ollama, or a LiteLLM proxy, once. Then authorize it for each Project on its first launch. Subscription logins stay in the harness and are never imported.</p>
 {connections.isPending && <p role="status">Loading connections…</p>}
 {connections.isError && <p role="alert">{normalizeAPIError(connections.error).message}</p>}
 {connections.data && !connections.data.enabled && <p>Reusable connections require an HTTPS Meridian address reachable from Capsules. Ask the installation administrator to enable the provider gateway.</p>}
 {connections.data?.enabled && <form onSubmit={event=>{event.preventDefault();save.mutate({id:editing?.id,body:body()});}}>
 <label>Provider<select value={provider} disabled={!!editing} onChange={event=>{setProvider(event.target.value as ProviderConnectionType);setPrivateHttp(false);}}>{(Object.keys(providerLabels) as ProviderConnectionType[]).map(value=><option key={value} value={value}>{providerLabels[value]}</option>)}</select></label>
 <label>Connection name<input value={name} maxLength={128} onChange={event=>setName(event.target.value)}/></label>
 {custom && <>
 <p>{provider==="openai-compatible"?"Codex, OpenCode, and Pi use this endpoint. Codex needs the Responses API (/v1/responses); OpenCode and Pi use Chat Completions and need a default model.":"Claude Code uses this endpoint through the Anthropic Messages API."} Meridian sends requests from the server; Capsules never see this address or key.</p>
 <label>Base URL<input value={baseUrl} required maxLength={2048} placeholder={privateHttp?"http://192.168.1.20:8000":"https://models.example.com"} onChange={event=>setBaseUrl(event.target.value)}/></label>
 <label>Default model<input value={model} maxLength={256} placeholder="Qwen/Qwen3-Coder-30B-A3B-Instruct" onChange={event=>setModel(event.target.value)}/></label>
 {model && <fieldset className="effort-levels"><legend>Reasoning effort levels (optional)</legend><p>Check the levels this model accepts. Harnesses then offer them, plus off, in the composer's model picker and send the chosen level as the request's reasoning effort.</p>{effortLevels.map(level=><label key={level}><input type="checkbox" checked={efforts.includes(level)} onChange={event=>setEfforts(current=>event.target.checked?[...current,level]:current.filter(value=>value!==level))}/>{effortLabels[level]}</label>)}</fieldset>}
 {privateHttpAllowed && <label><input type="checkbox" checked={privateHttp} onChange={event=>setPrivateHttp(event.target.checked)}/>Use plain HTTP to a private-network server</label>}
 {privateHttp && <p role="note">Plain HTTP sends prompts, code, and any API key unencrypted between Meridian and the model server. Use it only on a network you trust, such as a LAN or tailnet. Meridian connects only to loopback and private addresses in this mode.</p>}
 </>}
 {editing && custom && <p>Saving replaces the stored key. Enter it again, or leave it empty if the server needs none.</p>}
 <label>{custom?"API key (optional)":"API key"}<input type="password" autoComplete="off" value={key} required={!custom} maxLength={4096} onChange={event=>setKey(event.target.value)}/></label>
 <button disabled={save.isPending || (!custom && !key) || (custom && !baseUrl)}>{editing?(custom?"Save endpoint":"Replace API key"):"Save connection"}</button>{editing && <button type="button" onClick={reset}>Cancel</button>}</form>}
 {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
 <ul>{connections.data?.items.map(item=><li key={item.id}>{item.name} · {providerLabels[item.provider]}{item.baseUrl?` · ${item.baseUrl}`:""}{item.defaultModel?` · ${item.defaultModel}`:""}{item.reasoningEfforts?.length?` · effort ${item.reasoningEfforts.join("/")}`:""} · {item.revoked?"Revoked":"Saved"} <button onClick={()=>{setEditing(item);setProvider(item.provider);setName(item.name);setKey("");setBaseUrl(item.baseUrl??"");setModel(item.defaultModel??"");setPrivateHttp(!!item.allowPrivateHttp);setEfforts(item.reasoningEfforts??[]);}}>Reconnect</button> {!item.revoked && <button disabled={save.isPending} onClick={()=>save.mutate({id:item.id,body:{provider:item.provider,name:item.name,apiKey:"",expectedResourceVersion:item.resourceVersion,revoked:true}})}>Revoke</button>}</li>)}</ul>
 </section>;
}
