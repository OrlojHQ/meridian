import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { ChatGptSignIn, ProviderConnection, ProviderConnectionRequestWritable, ProviderConnectionType, ReasoningEffortLevel } from "../api/generated/types.gen";

const providerLabels:Record<ProviderConnectionType,string>={openai:"OpenAI",anthropic:"Anthropic","openai-compatible":"OpenAI-compatible endpoint","anthropic-compatible":"Anthropic-compatible endpoint",chatgpt:"ChatGPT plan"};
// ChatGPT connections come only from Sign in with ChatGPT, not this form.
const formProviders=(Object.keys(providerLabels) as ProviderConnectionType[]).filter(value=>value!=="chatgpt");
const isCustom=(provider:ProviderConnectionType)=>provider==="openai-compatible"||provider==="anthropic-compatible";

// Mirrors harnesssetup.ConnectionHarness: capsuled writes provider configuration only for these harnesses.
export const connectableHarnesses=["opencode", "codex", "claude", "pi", "dsh"];

// Mirrors harnesssetup.ConnectionSupported; the server enforces the same pairing.
export function connectionSupports(harness:string,item:ProviderConnection){
 switch(harness){
 case "claude": return item.provider==="anthropic"||item.provider==="anthropic-compatible";
 case "codex": return item.provider==="openai"||item.provider==="openai-compatible"||item.provider==="chatgpt";
 case "opencode": case "pi": return item.provider==="openai"||item.provider==="anthropic"||(item.provider==="openai-compatible"&&!!item.defaultModel);
 case "dsh": return isCustom(item.provider)&&!!item.defaultModel;
 default: return !isCustom(item.provider)&&item.provider!=="chatgpt";
 }
}

const effortLevels:ReasoningEffortLevel[]=["minimal","low","medium","high","xhigh","max"];
const effortLabels:Record<ReasoningEffortLevel,string>={minimal:"Minimal",low:"Low",medium:"Medium",high:"High",xhigh:"Extra high",max:"Max"};

// ChatGPTSignInPanel runs one Sign in with ChatGPT attempt. The daemon holds
// the PKCE verifier and receives the token; this page only shows OpenAI's
// authorization link and relays the redirected address when the daemon
// cannot catch it itself.
function ChatGPTSignInPanel({connection,autoStart,onDone}:{connection?:ProviderConnection;autoStart?:boolean;onDone:()=>void}){
 const cache=useQueryClient();
 const [callback,setCallback]=useState("");
 const start=useMutation({mutationFn:()=>api.startChatGPTSignIn(connection?.id)});
 const started=useRef(false);
 useEffect(()=>{if(autoStart&&!started.current){started.current=true;start.mutate();}},[autoStart,start]);
 const signIn=start.data;
 const status=useQuery({queryKey:["chatgpt-sign-in",signIn?.id],queryFn:({signal})=>api.chatGPTSignIn(signIn!.id,signal),enabled:!!signIn,refetchInterval:query=>query.state.data?.state==="pending"||!query.state.data?2000:false,gcTime:0});
 const complete=useMutation({mutationFn:(address:string)=>api.completeChatGPTSignIn(signIn!.id,address),onSuccess:()=>cache.invalidateQueries({queryKey:["chatgpt-sign-in",signIn?.id]})});
 const current:ChatGptSignIn|undefined=complete.data??status.data??signIn;
 const finished=current?.state==="completed";
 useEffect(()=>{if(finished)void cache.invalidateQueries({queryKey:["provider-connections"]});},[finished,cache]);
 if(!signIn){
  return <div className="chatgpt-sign-in">
   <button type="button" disabled={start.isPending} onClick={()=>start.mutate()}>{connection?"Sign in to ChatGPT again":"Continue with ChatGPT"}</button>
   {start.isError && <p role="alert">{normalizeAPIError(start.error).message}</p>}
  </div>;
 }
 if(finished){
  return <div className="chatgpt-sign-in" role="status"><p>Signed in to ChatGPT. Codex Capsules in Projects you authorize now use your plan without signing in.</p><button type="button" onClick={onDone}>Done</button></div>;
 }
 return <div className="chatgpt-sign-in">
  <p><a href={signIn.authorizationUrl} target="_blank" rel="noopener noreferrer">Open ChatGPT sign-in</a> and approve Meridian. This page updates when you finish.</p>
  {current?.state==="failed" && <p role="alert">{current.failure||"ChatGPT sign-in did not complete."} Start again.</p>}
  <form onSubmit={event=>{event.preventDefault();complete.mutate(callback);}}>
   <label>{signIn.listening?"If the page after approving doesn't load, paste its address":"After approving, paste the address of the page that doesn't load"}<input value={callback} maxLength={8192} autoComplete="off" spellCheck={false} placeholder="http://127.0.0.1:1455/auth/callback?code=…" onChange={event=>setCallback(event.target.value)}/></label>
   <button disabled={!callback||complete.isPending}>Finish sign-in</button> <button type="button" onClick={onDone}>Cancel</button>
  </form>
  {complete.isError && <p role="alert">{normalizeAPIError(complete.error).message}</p>}
 </div>;
}

function ChatGPTSection({enabled,available}:{enabled:boolean;available:boolean}){
 const [attempt,setAttempt]=useState(0);
 if(!enabled) return null;
 return <section aria-labelledby="chatgpt-heading" className="chatgpt-connection">
  <h3 id="chatgpt-heading">ChatGPT plan</h3>
  <p>Sign in once so Codex uses your ChatGPT plan in every Capsule, for terminals and Threads. Meridian keeps the login encrypted on this installation and gives Capsules only a short-lived gateway token. Use this only on a Meridian you alone control, and don't share it with other people.</p>
  {!available ? <p>Sign in with ChatGPT is unavailable on this installation.</p> : <ChatGPTSignInPanel key={attempt} onDone={()=>setAttempt(value=>value+1)}/>}
 </section>;
}

export function ProviderConnections(){
 const cache=useQueryClient();const connections=useQuery({queryKey:["provider-connections"],queryFn:api.providerConnections});
 const [provider,setProvider]=useState<ProviderConnectionType>("openai");const [name,setName]=useState("");const [key,setKey]=useState("");const [editing,setEditing]=useState<ProviderConnection>();
 const [reauthorizing,setReauthorizing]=useState<string>();
 const [baseUrl,setBaseUrl]=useState("");const [model,setModel]=useState("");const [privateHttp,setPrivateHttp]=useState(false);const [efforts,setEfforts]=useState<ReasoningEffortLevel[]>([]);
 const custom=isCustom(provider);const privateHttpAllowed=!!connections.data?.privateHttpAllowed;
 const reset=()=>{setKey("");setEditing(undefined);setBaseUrl("");setModel("");setPrivateHttp(false);setEfforts([]);};
 const save=useMutation({mutationFn:({body,id}:{body:ProviderConnectionRequestWritable,id?:string})=>api.saveProviderConnection(body,id),onSuccess:()=>{reset();save.reset();return cache.invalidateQueries({queryKey:["provider-connections"]});}});
 const body=():ProviderConnectionRequestWritable=>({name:name||(custom?providerLabels[provider]:`${provider} API`),provider,apiKey:key,expectedResourceVersion:editing?.resourceVersion??0,...(custom?{baseUrl,defaultModel:model||undefined,allowPrivateHttp:privateHttp,...(model&&efforts.length>0?{reasoningEfforts:effortLevels.filter(level=>efforts.includes(level))}:{})}:{})});
 return <section className="panel"><h2>Provider connections</h2><p>Sign in with ChatGPT, or connect your own OpenAI or Anthropic API key or a self-hosted model server such as vLLM, Ollama, or a LiteLLM proxy, once. Then authorize it for each Project on its first launch. Other subscription logins, such as Claude, stay in the harness and are never imported.</p>
 {connections.isPending && <p role="status">Loading connections…</p>}
 {connections.isError && <p role="alert">{normalizeAPIError(connections.error).message}</p>}
 {connections.data && !connections.data.enabled && <p>Reusable connections need the provider gateway, which is off on this installation. On Docker, meridiand starts it on its own unless the engine is remote or rootless; the daemon log says why it did not. Otherwise ask the installation administrator to set --provider-gateway-listen or --provider-gateway-url.</p>}
 {connections.data && <ChatGPTSection enabled={connections.data.enabled} available={!!connections.data.chatgptAvailable}/>}
 {connections.data?.enabled && <form onSubmit={event=>{event.preventDefault();save.mutate({id:editing?.id,body:body()});}}>
 <label>Provider<select value={provider} disabled={!!editing} onChange={event=>{setProvider(event.target.value as ProviderConnectionType);setPrivateHttp(false);}}>{formProviders.map(value=><option key={value} value={value}>{providerLabels[value]}</option>)}</select></label>
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
 <ul>{connections.data?.items.map(item=>item.provider==="chatgpt"?<li key={item.id}>{item.name} · {providerLabels[item.provider]}{item.account?` · ${item.account}`:""} · {item.revoked?"Revoked":item.needsSignIn?"Sign-in expired":"Signed in"}
  {item.revoked && item.revocationUnconfirmed && <p role="note">OpenAI did not confirm the sign-out. Disconnect Meridian in ChatGPT's settings to be sure.</p>}
  {!item.revoked && (reauthorizing===item.id ? <ChatGPTSignInPanel connection={item} autoStart onDone={()=>setReauthorizing(undefined)}/> : <button onClick={()=>setReauthorizing(item.id)}>{item.needsSignIn?"Sign in again":"Reconnect"}</button>)}
  {" "}{!item.revoked && <button disabled={save.isPending} onClick={()=>save.mutate({id:item.id,body:{provider:item.provider,name:item.name,apiKey:"",expectedResourceVersion:item.resourceVersion,revoked:true}})}>Sign out</button>}
 </li>:<li key={item.id}>{item.name} · {providerLabels[item.provider]}{item.baseUrl?` · ${item.baseUrl}`:""}{item.defaultModel?` · ${item.defaultModel}`:""}{item.reasoningEfforts?.length?` · effort ${item.reasoningEfforts.join("/")}`:""} · {item.revoked?"Revoked":"Saved"} <button onClick={()=>{setEditing(item);setProvider(item.provider);setName(item.name);setKey("");setBaseUrl(item.baseUrl??"");setModel(item.defaultModel??"");setPrivateHttp(!!item.allowPrivateHttp);setEfforts(item.reasoningEfforts??[]);}}>Reconnect</button> {!item.revoked && <button disabled={save.isPending} onClick={()=>save.mutate({id:item.id,body:{provider:item.provider,name:item.name,apiKey:"",expectedResourceVersion:item.resourceVersion,revoked:true}})}>Revoke</button>}</li>)}</ul>
 </section>;
}
