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
async function openHarness(name: string) {
  await userEvent.click(await screen.findByRole("button", { name }));
}
afterEach(()=>{cleanup();vi.restoreAllMocks();localStorage.clear();});
it("shows saved revision contents and rolls back only future launches",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[setup]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:false});
 vi.spyOn(api,"harnessSetupRevisions").mockResolvedValue({items:[{id:"rev-1",setupId:setup.id,digest:"abc",files:[".codex/AGENTS.md"],createdAt:setup.createdAt}]});
 const mutate=vi.spyOn(api,"mutateHarnessSetup").mockResolvedValue({...setup,revision:"rev-1",resourceVersion:3});
 show(<HarnessSetups/>);await openHarness("Manage Codex");await userEvent.click(await screen.findByRole("button",{name:"Files and history"}));
 await userEvent.click(await screen.findByText(/1 file · Current|1 file$/));expect(screen.getByText(".codex/AGENTS.md")).toBeInTheDocument();
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
 expect(await screen.findByRole("button",{name:"Start mock"})).toBeInTheDocument();
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
 expect(screen.getByRole("heading",{level:1,name:"Harnesses"})).toBeInTheDocument();
 await openHarness("Manage Codex");
 expect(await screen.findByRole("heading",{level:3,name:/My Codex setup/})).toBeInTheDocument();
 expect(screen.getAllByRole("heading",{level:2}).map(heading=>heading.textContent)).toEqual(["Codex","Import from this computer","Provider connections"]);
 expect(view.container.querySelector("details")).toBeNull();
 expect(screen.getByRole("button",{name:"Add skills folder"})).toBeInTheDocument();
 expect(view.container).toHaveTextContent("Dependency folders such as node_modules and login caches are excluded; you sign in to each harness separately.");
 expect(view.container).toHaveTextContent("Review import uploads the selected files to your Meridian server for checks.");
 expect(view.container).toHaveTextContent("Sign in with ChatGPT or Claude, or connect an API key or a self-hosted model server such as vLLM, Ollama, or a LiteLLM proxy, once. Then authorize it for each Project.");
 expect(view.container).not.toHaveTextContent("Capsule never receives");
 expect(view.container).not.toHaveTextContent("subscription logins stay");
 expect(view.container).toHaveTextContent("meridian harness import");
 expect(screen.getByLabelText("API key")).toHaveAttribute("type","password");
});
it("saves a self-hosted endpoint with an explicit private HTTP opt-in",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true,privateHttpAllowed:true});
 const save=vi.spyOn(api,"saveProviderConnection").mockResolvedValue({id:"c",provider:"openai-compatible",name:"DGX",createdAt:setup.createdAt,resourceVersion:1,revoked:false});
 show(<HarnessSetups/>);
 await openHarness("Set up Codex");
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
 await openHarness("Set up Codex");
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
it("signs in to Claude from the settings page and does not show a token",async()=>{
 const user=userEvent.setup();
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});
 vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true});
 const popup={closed:false,close:vi.fn(),location:{href:""}} as unknown as Window;
 vi.spyOn(window,"open").mockReturnValue(popup);
 const begin=vi.spyOn(api,"beginClaudeLogin").mockResolvedValue({loginId:"login-1",authorizeUrl:"https://claude.ai/oauth/authorize?code_challenge=abc&state=state-1",expiresAt:setup.createdAt,manual:false});
 vi.spyOn(api,"getClaudeLogin").mockResolvedValue({status:"complete",connectionId:"claude-login"});
 show(<HarnessSetups/>);
 await openHarness("Set up Claude Code");
 await user.selectOptions(await screen.findByLabelText("Provider"),"anthropic");
 const signIn=screen.getByRole("button",{name:"Sign in with Claude"});
 expect(signIn.querySelector("img")).toHaveAttribute("alt","");
 expect(screen.getByText("Sign in with your Claude account.")).toBeInTheDocument();
 expect(screen.queryByText(/gateway lease|setup-token/)).toBeNull();
 expect(screen.queryByLabelText("Claude login")).toBeNull();
 await user.click(screen.getByRole("button",{name:"Sign in with Claude"}));
 await waitFor(()=>expect(begin).toHaveBeenCalledWith({}));
 expect(window.open).toHaveBeenCalledWith("about:blank","meridian-claude-login","popup,width=520,height=760");
 await waitFor(()=>expect(popup.location.href).toBe("https://claude.ai/oauth/authorize?code_challenge=abc&state=state-1"));
 await waitFor(()=>expect(screen.getByRole("status")).toHaveTextContent("Signed in. Meridian stored the login encrypted and will not show it."));
 expect(document.body.textContent).not.toContain("sk-ant");
 expect(popup.close).toHaveBeenCalled();
});
it("saves, replaces, and clears a pasted Claude login without showing it again",async()=>{
 const user=userEvent.setup();
 const saved={id:"claude-login",provider:"anthropic" as const,name:"Claude",credentialKind:"oauth" as const,createdAt:setup.createdAt,resourceVersion:1,revoked:false};
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});
 vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true});
 const save=vi.spyOn(api,"saveProviderConnection").mockResolvedValue(saved);
 show(<HarnessSetups/>);
 await openHarness("Set up Claude Code");
 await user.selectOptions(await screen.findByLabelText("Provider"),"anthropic");
 await user.click(screen.getByRole("button",{name:"Use an API key or a setup token instead"}));
 expect(screen.queryByText("Fallback credential")).toBeNull();
 expect(screen.queryByLabelText("Claude login")).toBeNull();
 expect(screen.getByRole("radio",{name:"API key"})).toBeChecked();
 await user.click(screen.getByRole("radio",{name:"Setup token"}));
 const secret="sk-ant-oat-claude-login-canary";
 await user.type(screen.getByLabelText("Setup token",{selector:"input[type='password']"}),secret);
 await user.click(screen.getByRole("button",{name:"Save setup token"}));
 await waitFor(()=>expect(save).toHaveBeenCalledWith({name:"Claude",provider:"anthropic",apiKey:secret,credentialKind:"oauth",expectedResourceVersion:0},undefined));
 expect(screen.queryByDisplayValue(secret)).toBeNull();
 expect(document.body.textContent).not.toContain(secret);
 save.mockResolvedValue({...saved,resourceVersion:2});
 vi.spyOn(api,"providerConnections").mockResolvedValue({items:[saved],enabled:true});
 cleanup();
 show(<HarnessSetups/>);
 await openHarness("Manage Claude Code");
 expect(await screen.findByText(/Signed in/)).toBeInTheDocument();
 expect(document.body.textContent).not.toContain(secret);
 await user.click(screen.getByRole("button",{name:"Replace login"}));
 expect(screen.getByLabelText("Setup token",{selector:"input[type='password']"})).toHaveValue("");
 expect(screen.queryByLabelText("Claude login")).toBeNull();
 expect(screen.getByRole("radio",{name:"Setup token"})).toBeChecked();
 const replacement="sk-ant-oat-claude-login-replaced";
 await user.type(screen.getByLabelText("Setup token",{selector:"input[type='password']"}),replacement);
 await user.click(screen.getByRole("button",{name:"Replace setup token"}));
 await waitFor(()=>expect(save).toHaveBeenCalledWith({name:"Claude",provider:"anthropic",apiKey:replacement,credentialKind:"oauth",expectedResourceVersion:1},"claude-login"));
 expect(screen.queryByDisplayValue(replacement)).toBeNull();
 await user.click(screen.getByRole("button",{name:"Clear login"}));
 await waitFor(()=>expect(save).toHaveBeenCalledWith({provider:"anthropic",name:"Claude",apiKey:"",expectedResourceVersion:1,revoked:true},"claude-login"));
});
it("hides the private HTTP opt-in unless the daemon allows it",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true});
 show(<HarnessSetups/>);
 await openHarness("Set up Codex");
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
it("lists each harness with its mark, what a new Capsule uses, and one action",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[setup]});
 vi.spyOn(api,"providerConnections").mockResolvedValue({enabled:true,items:[
  {id:"plan",provider:"chatgpt",name:"Ada's ChatGPT",createdAt:setup.createdAt,resourceVersion:1,revoked:false},
  {id:"claude",provider:"anthropic",name:"Claude login",createdAt:setup.createdAt,resourceVersion:1,revoked:false,needsSignIn:true},
 ]});
 show(<HarnessSetups/>);
 const codex=(await screen.findByRole("button",{name:"Manage Codex"})).closest("li")!;
 expect(codex.querySelector(".harness-mark-codex svg")).not.toBeNull();
 expect(codex).toHaveTextContent("Setup “My Codex setup”");
 expect(codex).toHaveTextContent("Ada's ChatGPT");
 expect(within(codex).getByRole("button")).toHaveTextContent(/^Manage$/);
 const claude=screen.getByRole("button",{name:"Manage Claude Code"}).closest("li")!;
 expect(claude).toHaveTextContent("No setup imported");
 expect(claude).toHaveTextContent("Sign-in expired");
 const dsh=screen.getByRole("button",{name:"Set up DeepSeek Harness"}).closest("li")!;
 expect(dsh.querySelector(".harness-mark-dsh svg")).not.toBeNull();
 expect(dsh).toHaveTextContent("No provider connection");
 expect(screen.queryByText("Not set up")).toBeNull();
});
it("opens provider connections from the link and stays closed after Close or Escape",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});
 vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:true});
 render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><MemoryRouter initialEntries={["/ui/settings/harnesses#provider-connections"]}><HarnessSetups/></MemoryRouter></QueryClientProvider>);
 const drawer=await screen.findByRole("dialog",{name:"Connect a provider"});
 expect(within(drawer).getByRole("heading",{name:"Provider connections"})).toBeInTheDocument();
 // Connections are installation-wide; the link does not open a harness import.
 expect(within(drawer).queryByText("Import from this computer")).toBeNull();
 await userEvent.click(within(drawer).getByRole("button",{name:"Close"}));
 await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());
 // A later render must not reopen it from the hash.
 await userEvent.click(screen.getByRole("button",{name:"Set up Codex"}));
 expect(await screen.findByRole("dialog",{name:"Codex settings"})).toBeInTheDocument();
 await userEvent.keyboard("{Escape}");
 await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());
 await new Promise(resolve=>setTimeout(resolve, 50));
 expect(screen.queryByRole("dialog")).toBeNull();
});
it("leads a saved setup with editing and re-importing, and renames inline",async()=>{
 vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[setup]});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:false});
 const mutate=vi.spyOn(api,"mutateHarnessSetup").mockResolvedValue({...setup,name:"Work Codex",resourceVersion:3});
 show(<HarnessSetups/>);await openHarness("Manage Codex");
 const card=await screen.findByRole("region",{name:"My Codex setup"});
 expect(card).toHaveTextContent("New Codex Capsules start with this setup.");
 expect(within(card).getByRole("button",{name:"Edit files and skills"})).toHaveClass("primary-button");
 expect(within(card).getByRole("button",{name:"Update from this computer"})).toBeInTheDocument();
 // The import panel says it updates this setup, and keeps the skills folder button.
 const importPanel=document.getElementById("import-setup")!;
 expect(importPanel).toHaveTextContent("Import again to update My Codex setup.");
 expect(within(importPanel).getByRole("button",{name:"Add skills folder"})).toBeInTheDocument();
 // No name field until Rename is chosen.
 expect(within(card).queryByLabelText("Setup name")).toBeNull();
 await userEvent.click(within(card).getByRole("button",{name:"Rename"}));
 const field=within(card).getByLabelText("Setup name");
 await userEvent.clear(field);await userEvent.type(field,"Work Codex{Enter}");
 await waitFor(()=>expect(mutate).toHaveBeenCalledWith("setup",{name:"Work Codex",expectedResourceVersion:2}));
});
