import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { ProviderConnection, ProviderConnectionRequestWritable, ProviderConnectionType, ReasoningEffortLevel } from "../api/generated/types.gen";
// Official Claude mark, unchanged, from https://claude.com/icon.svg.
// It only identifies the sign-in. Anthropic's mark stays its own.
import claudeMark from "../assets/claude-mark.svg";

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
 const [loginKind,setLoginKind]=useState<"api_key"|"oauth">("api_key");
 const [fallback,setFallback]=useState(false);const [busy,setBusy]=useState(false);const [message,setMessage]=useState("");const [continueURL,setContinueURL]=useState("");
 const [active,setActive]=useState<{id:string,manual:boolean}|undefined>();const [wantCode,setWantCode]=useState(false);const [code,setCode]=useState("");
 const polls=useRef(0);
 useEffect(()=>()=>{polls.current++;},[]);
 const custom=isCustom(provider);const claude=provider==="anthropic";const privateHttpAllowed=!!connections.data?.privateHttpAllowed;
 const reset=()=>{setKey("");setEditing(undefined);setBaseUrl("");setModel("");setPrivateHttp(false);setEfforts([]);setLoginKind("api_key");setFallback(false);};
 const save=useMutation({mutationFn:({body,id}:{body:ProviderConnectionRequestWritable,id?:string})=>api.saveProviderConnection(body,id),onSuccess:()=>{reset();save.reset();return cache.invalidateQueries({queryKey:["provider-connections"]});}});
 const body=():ProviderConnectionRequestWritable=>({name:name||(custom?providerLabels[provider]:claude?"Claude":`${provider} API`),provider,apiKey:key,expectedResourceVersion:editing?.resourceVersion??0,...(claude&&loginKind==="oauth"?{credentialKind:"oauth"}:{}),...(custom?{baseUrl,defaultModel:model||undefined,allowPrivateHttp:privateHttp,...(model&&efforts.length>0?{reasoningEfforts:effortLevels.filter(level=>efforts.includes(level))}:{})}:{})});
 async function signIn(connectionId?:string){
  const generation=++polls.current;
  const popup=window.open("about:blank","meridian-claude-login","popup,width=520,height=760");
  setBusy(true);setMessage("");setWantCode(false);setContinueURL("");
  try{
   const started=await api.beginClaudeLogin(connectionId?{connectionId}:{});
   if(generation!==polls.current){popup?.close();return;}
   if(popup&&!popup.closed)popup.location.href=started.authorizeUrl;
   else setContinueURL(started.authorizeUrl);
   setActive({id:started.loginId,manual:started.manual});
   const deadline=Date.now()+10*60*1000;
   while(Date.now()<deadline&&generation===polls.current){
    const status=await api.getClaudeLogin(started.loginId);
    if(generation!==polls.current)return;
    if(status.status==="complete"){
     popup?.close();setContinueURL("");setActive(undefined);setCode("");
     setMessage("Signed in. Meridian stored the login encrypted and will not show it.");
     await cache.invalidateQueries({queryKey:["provider-connections"]});
     return;
    }
    if(status.status==="failed"){setMessage("Claude sign-in failed. Try again.");return;}
    await new Promise(resolve=>setTimeout(resolve,1000));
   }
  }catch(error){if(generation===polls.current)setMessage(normalizeAPIError(error).message);}
  finally{if(generation===polls.current)setBusy(false);}
 }
 async function finish(){
  if(!active)return;
  const value=code.trim();
  setCode("");setBusy(true);
  try{
   await api.finishClaudeLogin(active.id,value);
   setActive(undefined);setWantCode(false);setContinueURL("");
   setMessage("Signed in. Meridian stored the login encrypted and will not show it.");
   await cache.invalidateQueries({queryKey:["provider-connections"]});
  }catch(error){setMessage(normalizeAPIError(error).message);}
  finally{setBusy(false);}
 }
 const savedKind=loginKind==="oauth"?"setup token":"API key";
 return <section className="panel"><h2>Provider connections</h2><p>Connect a provider once, then authorize it for each Project.</p>
 {connections.isPending && <p role="status">Loading connections…</p>}
 {connections.isError && <p role="alert">{normalizeAPIError(connections.error).message}</p>}
 {connections.data && !connections.data.enabled && <p>Reusable connections need the provider gateway, which is off on this installation. On Docker, meridiand starts it on its own unless the engine is remote or rootless; the daemon log says why it did not. Otherwise ask the installation administrator to set --provider-gateway-listen or --provider-gateway-url.</p>}
 {connections.data?.enabled && <form onSubmit={event=>{event.preventDefault();if(claude&&!fallback)return;if(!custom&&!key)return;save.mutate({id:editing?.id,body:body()});}}>
 <label>Provider<select value={provider} disabled={!!editing} onChange={event=>{setProvider(event.target.value as ProviderConnectionType);setPrivateHttp(false);setLoginKind("api_key");setFallback(false);}}>{(Object.keys(providerLabels) as ProviderConnectionType[]).map(value=><option key={value} value={value}>{providerLabels[value]}</option>)}</select></label>
 <label>Connection name<input value={name} maxLength={128} onChange={event=>setName(event.target.value)}/></label>
 {custom && <>
 <p>{provider==="openai-compatible"?"Codex, OpenCode, and Pi use this endpoint. Codex needs the Responses API (/v1/responses); OpenCode and Pi use Chat Completions and need a default model.":"Claude Code uses this endpoint through the Anthropic Messages API."} Meridian sends requests from the server; Capsules never see this address or key.</p>
 <label>Base URL<input value={baseUrl} required maxLength={2048} placeholder={privateHttp?"http://192.168.1.20:8000":"https://models.example.com"} onChange={event=>setBaseUrl(event.target.value)}/></label>
 <label>Default model<input value={model} maxLength={256} placeholder="Qwen/Qwen3-Coder-30B-A3B-Instruct" onChange={event=>setModel(event.target.value)}/></label>
 {model && <fieldset className="effort-levels"><legend>Reasoning effort levels (optional)</legend><p>Check the levels this model accepts. Harnesses then offer them, plus off, in the composer's model picker and send the chosen level as the request's reasoning effort.</p>{effortLevels.map(level=><label key={level}><input type="checkbox" checked={efforts.includes(level)} onChange={event=>setEfforts(current=>event.target.checked?[...current,level]:current.filter(value=>value!==level))}/>{effortLabels[level]}</label>)}</fieldset>}
 {privateHttpAllowed && <label><input type="checkbox" checked={privateHttp} onChange={event=>setPrivateHttp(event.target.checked)}/>Use plain HTTP to a private-network server</label>}
 {privateHttp && <p role="note">Plain HTTP sends prompts, code, and any API key unencrypted between Meridian and the model server. Use it only on a network you trust, such as a LAN or tailnet. Meridian connects only to loopback and private addresses in this mode.</p>}
 </>}
 {claude && <div className="claude-login">
 <p>Sign in with your Claude account.</p>
 <button type="button" className="primary-button claude-sign-in" disabled={busy} onClick={()=>void signIn(editing?.provider==="anthropic"?editing.id:undefined)}><img src={claudeMark} alt="" />{editing?"Sign in again":"Sign in with Claude"}</button>
 {continueURL && <p><a href={continueURL} target="_blank" rel="noopener noreferrer">Continue to Claude</a></p>}
 {active&&(active.manual||wantCode) && <><p>{active.manual?"This browser cannot return here automatically. Paste the code Claude shows. Meridian exchanges it and does not keep the code.":"Paste the code Claude shows if the window did not return. Meridian exchanges it and does not keep the code."}</p><label>Authorization code<input type="password" autoComplete="off" value={code} maxLength={2048} onChange={event=>setCode(event.target.value)}/></label><button type="button" disabled={busy||!code.trim()} onClick={()=>void finish()}>Finish sign-in</button></>}
 {active&&!active.manual&&!wantCode && <button type="button" className="secondary" onClick={()=>setWantCode(true)}>Enter the code from Claude</button>}
 {message && <p role="status">{message}</p>}
 {!fallback && <button type="button" className="secondary" onClick={()=>setFallback(true)}>Use an API key or a setup token instead</button>}
 {fallback && <div className="claude-login-fallback">
  <div className="choice-row">
   <label><input type="radio" name="claude-login-kind" checked={loginKind==="api_key"} onChange={()=>setLoginKind("api_key")}/>API key</label>
   <label><input type="radio" name="claude-login-kind" checked={loginKind==="oauth"} onChange={()=>setLoginKind("oauth")}/>Setup token</label>
  </div>
  <input type="password" aria-label={loginKind==="api_key"?"API key":"Setup token"} autoComplete="off" value={key} maxLength={4096} onChange={event=>setKey(event.target.value)}/>
  <button disabled={save.isPending||!key}>{editing?`Replace ${savedKind}`:`Save ${savedKind}`}</button>
 </div>}
</div>}
 {editing && custom && <p>Saving replaces the stored key. Enter it again, or leave it empty if the server needs none.</p>}
 {!claude && <label>{custom?"API key (optional)":"API key"}<input type="password" autoComplete="off" value={key} required={!custom} maxLength={4096} onChange={event=>setKey(event.target.value)}/></label>}
 {!claude && <button disabled={save.isPending || (!custom && !key) || (custom && !baseUrl)}>{editing?(custom?"Save endpoint":"Replace API key"):"Save connection"}</button>}
 {editing && <button type="button" onClick={reset}>Cancel</button>}
 </form>}
 {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
 <ul>{connections.data?.items.map(item=><li key={item.id}>{item.name} · {providerLabels[item.provider]}{item.provider==="anthropic"?` · ${item.credentialKind==="oauth"?"Signed in":"API key"}`:""}{item.baseUrl?` · ${item.baseUrl}`:""}{item.defaultModel?` · ${item.defaultModel}`:""}{item.reasoningEfforts?.length?` · effort ${item.reasoningEfforts.join("/")}`:""} · {item.revoked?"Revoked":"Saved"} {item.provider==="anthropic" && <button type="button" disabled={busy} onClick={()=>void signIn(item.id)}>Sign in again</button>} <button onClick={()=>{setEditing(item);setProvider(item.provider);setName(item.name);setKey("");setBaseUrl(item.baseUrl??"");setModel(item.defaultModel??"");setPrivateHttp(!!item.allowPrivateHttp);setEfforts(item.reasoningEfforts??[]);setLoginKind(item.credentialKind==="oauth"?"oauth":"api_key");setFallback(item.provider==="anthropic");}}>{item.provider==="anthropic"?"Replace login":"Reconnect"}</button> {!item.revoked && <button disabled={save.isPending} onClick={()=>save.mutate({id:item.id,body:{provider:item.provider,name:item.name,apiKey:"",expectedResourceVersion:item.resourceVersion,revoked:true}})}>{item.provider==="anthropic"?"Clear login":"Revoke"}</button>}</li>)}</ul>
 </section>;
}
