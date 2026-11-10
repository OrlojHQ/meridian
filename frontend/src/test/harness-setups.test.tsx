import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { HarnessSetups } from "../components/HarnessSetups";
import { NativeLauncher } from "../components/NativeLauncher";
import { StructuredLauncher } from "../components/StructuredLauncher";

const setup={id:"setup",harness:"codex",name:"My Codex setup",revision:"rev-2",default:true,deleted:false,createdAt:"2026-09-06T00:00:00Z",resourceVersion:2};
const project={id:"project",name:"Project",createdAt:setup.createdAt,updatedAt:setup.createdAt,resourceVersion:1,harnessImages:[{name:"codex",imageReference:"example/codex:v1"}]};
const capsule={id:"capsule",projectId:project.id,timelineId:"timeline",name:"generated",state:"Creating" as const,desiredState:"Ready" as const,restoreComplete:true,createdAt:setup.createdAt,updatedAt:setup.createdAt,resourceVersion:1};
function show(ui:React.ReactNode){return render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}})}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>);}
afterEach(()=>{cleanup();vi.restoreAllMocks();localStorage.clear();});
it("shows saved revision contents and rolls back only future launches",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[setup]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:false});
 vi.spyOn(api,"harnessSetupRevisions").mockResolvedValue({items:[{id:"rev-1",setupId:setup.id,digest:"abc",files:[".codex/AGENTS.md"],createdAt:setup.createdAt}]});
 const mutate=vi.spyOn(api,"mutateHarnessSetup").mockResolvedValue({...setup,revision:"rev-1",resourceVersion:3});
 show(<HarnessSetups/>);await userEvent.click(await screen.findByRole("button",{name:"Files and history"}));
 await userEvent.click(await screen.findByText(/1 files/));expect(screen.getByText(".codex/AGENTS.md")).toBeInTheDocument();
 await userEvent.click(screen.getByRole("button",{name:"Use this revision for new Capsules"}));await waitFor(()=>expect(mutate).toHaveBeenCalledWith("setup",{revision:"rev-1",expectedResourceVersion:2}));
});
it("launches with an automatic name and requires a deliberate new connection grant",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[setup]});vi.spyOn(api,"projectHarnessSetup").mockResolvedValue({setup:""});vi.spyOn(api,"projectConnection").mockResolvedValue({connectionId:""});
 vi.spyOn(api,"providerConnections").mockResolvedValue({enabled:true,items:[{id:"connection",provider:"openai",name:"My API",createdAt:setup.createdAt,resourceVersion:1,revoked:false}]});
 const grant=vi.spyOn(api,"grantConnection").mockResolvedValue({connectionId:"connection"});const create=vi.spyOn(api,"createCapsule").mockResolvedValue(capsule);vi.spyOn(api,"capsule").mockResolvedValue(capsule);vi.spyOn(api,"runs").mockResolvedValue({items:[]});
 show(<NativeLauncher project={project} harness="codex"/>);
 expect(await screen.findByText(/Using My Codex setup/)).toBeInTheDocument();expect(grant).not.toHaveBeenCalled();
 await userEvent.selectOptions(screen.getByLabelText("Provider connection"),"connection");expect(screen.getByText(/authorizes this API connection/)).toBeInTheDocument();
 await userEvent.click(screen.getByRole("button",{name:"Start Codex"}));await waitFor(()=>expect(create).toHaveBeenCalled());
 expect(grant).toHaveBeenCalledWith("project","codex","connection");expect(create).toHaveBeenCalledWith("project",expect.stringMatching(/^codex-/),"codex",undefined,"");
});
it("offers no provider connection to a harness capsuled cannot configure",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});vi.spyOn(api,"projectHarnessSetup").mockResolvedValue({setup:""});vi.spyOn(api,"projectConnection").mockResolvedValue({connectionId:""});
 vi.spyOn(api,"providerConnections").mockResolvedValue({enabled:true,items:[{id:"connection",provider:"anthropic",name:"My API",createdAt:setup.createdAt,resourceVersion:1,revoked:false}]});
 show(<NativeLauncher project={{...project,harnessImages:[{name:"mock",imageReference:"example/mock:v1"}]}} harness="mock"/>);
 expect(await screen.findByRole("button",{name:"Start Mock (test)"})).toBeInTheDocument();
 expect(screen.queryByLabelText("Provider connection")).toBeNull();expect(screen.queryByRole("link",{name:"Connect provider"})).toBeNull();
});
it("grants a custom endpoint connection to a DeepSeek Harness task before spawning it",async()=>{
 vi.spyOn(api,"capabilities").mockResolvedValue({providerVersion:"docker/v2",attach:true,run:true,structured:true,git:false,pause:false,snapshot:false,clone:false,browse:false,delivery:false,preview:false,resourceMetrics:false,harnessImages:[]});
 vi.spyOn(api,"projectConnection").mockResolvedValue({connectionId:""});
 vi.spyOn(api,"providerConnections").mockResolvedValue({enabled:true,items:[
  {id:"official",provider:"openai",name:"OpenAI",createdAt:setup.createdAt,resourceVersion:1,revoked:false},
  {id:"nomodel",provider:"openai-compatible",name:"No model",baseUrl:"https://models.example",createdAt:setup.createdAt,resourceVersion:1,revoked:false},
  {id:"dgx",provider:"openai-compatible",name:"DGX",baseUrl:"https://dgx.example",defaultModel:"deepseek-v4",createdAt:setup.createdAt,resourceVersion:1,revoked:false},
 ]});
 const grant=vi.spyOn(api,"grantConnection").mockResolvedValue({connectionId:"dgx"});
 const intent={id:"intent",projectId:project.id,harness:"dsh",state:"provisioning" as const,capsuleId:"capsule",capsuleName:"dsh-task",threadId:"thread",runId:"run",messageId:"message",createdAt:setup.createdAt,updatedAt:setup.createdAt,resourceVersion:1};
 const spawn=vi.spyOn(api,"createProjectThread").mockResolvedValue(intent);
 vi.spyOn(api,"projectThreadIntent").mockResolvedValue(intent);
 vi.spyOn(api,"capsule").mockResolvedValue(capsule);vi.spyOn(api,"runs").mockResolvedValue({items:[]});
 show(<StructuredLauncher project={{...project,harnessImages:[{name:"dsh",imageReference:"example/dsh:v1"}]}} harness="dsh"/>);
 const select=await screen.findByLabelText("Provider connection");
 await waitFor(()=>expect(screen.getByRole("option",{name:"DGX"})).toBeInTheDocument());
 expect(screen.queryByRole("option",{name:"OpenAI"})).toBeNull();expect(screen.queryByRole("option",{name:"No model"})).toBeNull();
 await userEvent.selectOptions(select,"dgx");await userEvent.type(screen.getByLabelText("Task"),"fix the build");
 await userEvent.click(screen.getByRole("button",{name:"Start task"}));
 await waitFor(()=>expect(spawn).toHaveBeenCalled());
 expect(grant).toHaveBeenCalledWith("project","dsh","dgx");expect(grant.mock.invocationCallOrder[0]).toBeLessThan(spawn.mock.invocationCallOrder[0]);
});
it("groups settings under plain section headings without nested disclosures",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[setup]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true});
 const view=show(<HarnessSetups/>);
 expect(screen.getByRole("heading",{level:1,name:"Harness settings"})).toBeInTheDocument();
 expect(await screen.findByRole("heading",{level:3,name:/My Codex setup/})).toBeInTheDocument();
 expect(screen.getAllByRole("heading",{level:2}).map(heading=>heading.textContent)).toEqual(["Your setups","Import from this computer","Provider connections"]);
 expect(view.container.querySelector("details")).toBeNull();
 expect(screen.getByRole("button",{name:"Add skills folder"})).toBeInTheDocument();
 expect(view.container).toHaveTextContent("Dependency folders such as node_modules and login caches are excluded; you sign in to each harness separately.");
 expect(view.container).toHaveTextContent("Review import uploads the selected files to your Meridian server for checks.");
 expect(view.container).toHaveTextContent("Other subscription logins, such as Claude, stay in the harness and are never imported.");
 expect(view.container).toHaveTextContent("meridian harness import");
 expect(screen.getByLabelText("API key")).toHaveAttribute("type","password");
});
it("saves a self-hosted endpoint with an explicit private HTTP opt-in",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true,privateHttpAllowed:true});
 const save=vi.spyOn(api,"saveProviderConnection").mockResolvedValue({id:"c",provider:"openai-compatible",name:"DGX",createdAt:setup.createdAt,resourceVersion:1,revoked:false});
 show(<HarnessSetups/>);
 await userEvent.selectOptions(await screen.findByLabelText("Provider"),"openai-compatible");
 expect(screen.getByLabelText("API key (optional)")).not.toBeRequired();
 await userEvent.type(screen.getByLabelText("Connection name"),"DGX");
 await userEvent.type(screen.getByLabelText("Base URL"),"http://192.168.1.20:8000");
 await userEvent.type(screen.getByLabelText("Default model"),"qwen3-coder");
 await userEvent.click(screen.getByLabelText("Use plain HTTP to a private-network server"));
 expect(screen.getByRole("note")).toHaveTextContent("unencrypted");
 await userEvent.click(screen.getByRole("button",{name:"Save connection"}));
 await waitFor(()=>expect(save).toHaveBeenCalledWith({name:"DGX",provider:"openai-compatible",apiKey:"",expectedResourceVersion:0,baseUrl:"http://192.168.1.20:8000",defaultModel:"qwen3-coder",allowPrivateHttp:true},undefined));
});
it("saves the reasoning effort levels a self-hosted model accepts",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true});
 const save=vi.spyOn(api,"saveProviderConnection").mockResolvedValue({id:"c",provider:"openai-compatible",name:"GLM",createdAt:setup.createdAt,resourceVersion:1,revoked:false});
 show(<HarnessSetups/>);
 await userEvent.selectOptions(await screen.findByLabelText("Provider"),"openai-compatible");
 expect(screen.queryByRole("group",{name:"Reasoning effort levels (optional)"})).toBeNull();
 await userEvent.type(screen.getByLabelText("Base URL"),"https://models.example.com");
 await userEvent.type(screen.getByLabelText("Default model"),"glm-5.3-flash");
 const levels=screen.getByRole("group",{name:"Reasoning effort levels (optional)"});
 await userEvent.click(within(levels).getByLabelText("Max"));
 await userEvent.click(within(levels).getByLabelText("Low"));
 await userEvent.click(within(levels).getByLabelText("High"));
 await userEvent.click(screen.getByRole("button",{name:"Save connection"}));
 await waitFor(()=>expect(save).toHaveBeenCalledWith(expect.objectContaining({defaultModel:"glm-5.3-flash",reasoningEfforts:["low","high","max"]}),undefined));
});
it("hides the private HTTP opt-in unless the daemon allows it",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true});
 show(<HarnessSetups/>);
 await userEvent.selectOptions(await screen.findByLabelText("Provider"),"anthropic-compatible");
 expect(screen.getByLabelText("Base URL")).toBeRequired();
 expect(screen.queryByLabelText("Use plain HTTP to a private-network server")).toBeNull();
});
it("offers only connections the harness can use",async()=>{
 const claudeProject={...project,harnessImages:[{name:"claude",imageReference:"example/claude:v1"}]};
 const item=(id:string,provider:"openai"|"anthropic"|"openai-compatible"|"anthropic-compatible",defaultModel?:string)=>({id,provider,name:id,createdAt:setup.createdAt,resourceVersion:1,revoked:false,...(defaultModel?{defaultModel}:{})});
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});vi.spyOn(api,"projectHarnessSetup").mockResolvedValue({setup:""});vi.spyOn(api,"projectConnection").mockResolvedValue({connectionId:""});
 vi.spyOn(api,"providerConnections").mockResolvedValue({enabled:true,items:[item("official-openai","openai"),item("vllm","openai-compatible"),item("litellm","anthropic-compatible","qwen")]});
 show(<NativeLauncher project={claudeProject} harness="claude"/>);
 const select=await screen.findByLabelText("Provider connection");
 await waitFor(()=>expect([...select.querySelectorAll("option")].map(option=>option.value)).toEqual(["","litellm"]));
});
