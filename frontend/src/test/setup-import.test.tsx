import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { HarnessSetupImport } from "../components/HarnessSetupImport";
import { NativeLauncher } from "../components/NativeLauncher";
import { selectLocalFiles } from "../import/localSetup";

const timestamp="2026-09-07T12:00:00Z";
const saved={id:"setup",harness:"opencode",name:"My OpenCode setup",revision:"rev",default:true,deleted:false,createdAt:timestamp,resourceVersion:1};
const bundle={harness:"opencode" as const,files:[{path:".config/opencode/opencode.json",content:'{"model":"openai/test"}',executable:false}],dependencies:[]};
function file(name:string,content:string,relative?:string){const result=new File([content],name,{type:"application/json"});Object.defineProperty(result,"arrayBuffer",{value:vi.fn().mockResolvedValue(new TextEncoder().encode(content).buffer)});if(relative)Object.defineProperty(result,"webkitRelativePath",{value:relative});return result;}
function show(ui:React.ReactNode){return render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}})}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>);}
afterEach(()=>{cleanup();vi.restoreAllMocks();localStorage.clear();});
it("reviews only eligible files, requires exclusion review, and retries the same save",async()=>{
 const raw=file("opencode.json",'{"model":"openai/test","provider":{"apiKey":"private"}}');
 const auth=file("auth.json",'{"access_token":"never-upload"}');
 const preview=vi.spyOn(api,"previewHarnessSetup").mockResolvedValue({bundle,issues:[{path:"opencode.json:provider",reason:"Credential setting excluded"}],digest:"digest"});
 const save=vi.spyOn(api,"importHarnessSetup").mockRejectedValueOnce(new TypeError("fetch failed")).mockResolvedValueOnce(saved);
 const done=vi.fn();show(<HarnessSetupImport setups={[]} onSaved={done}/>);
 await userEvent.upload(screen.getByLabelText("Choose configuration files"),[raw,auth]);
 expect(preview).not.toHaveBeenCalled();expect(auth.arrayBuffer).not.toHaveBeenCalled();
 await userEvent.click(screen.getByRole("button",{name:"Review import"}));
 expect(await screen.findByRole("heading",{name:"Review what will transfer"})).toBeInTheDocument();
 expect(preview).toHaveBeenCalledWith({harness:"opencode",files:[{path:bundle.files[0]!.path,content:'{"model":"openai/test","provider":{"apiKey":"private"}}',executable:false}]});
 expect(auth.arrayBuffer).not.toHaveBeenCalled();expect(screen.getByRole("button",{name:"Save setup"})).toBeDisabled();
 await userEvent.click(screen.getByLabelText("I reviewed the exclusions"));
 await userEvent.click(screen.getByRole("button",{name:"Save setup"}));
 expect(await screen.findByRole("alert")).toHaveTextContent("Unable to reach Meridian");
 await userEvent.click(screen.getByRole("button",{name:"Save setup"}));
 await waitFor(()=>expect(done).toHaveBeenCalledWith(saved));
 expect(save.mock.calls[0]?.[1]).toBe(save.mock.calls[1]?.[1]);
 expect(save.mock.calls[0]?.[0].bundle).toEqual(bundle);
 // After saving, the import steps stay, so more skills can be added at once.
 expect(screen.getByRole("status")).toHaveTextContent("Saved My OpenCode setup. New OpenCode Capsules start with it.");
 expect(screen.getByRole("button",{name:"Add skills folder"})).toBeInTheDocument();
 expect(screen.getByRole("button",{name:"Choose folder"})).toBeInTheDocument();
});
it("lists DeepSeek Harness and maps its local config files",()=>{
 show(<HarnessSetupImport setups={[]}/>);
 expect(screen.getByRole("option",{name:"DeepSeek Harness"})).toHaveValue("dsh");
 const skill=file("SKILL.md","instructions","dsh/skills/review/SKILL.md");
 const patch=file("cordis.patch.yml","- id: acp\n","dsh/cordis.patch.yml");
 const creds=file(".credentials.yaml","token: never-upload","dsh/.credentials.yaml");
 const selected=selectLocalFiles("dsh",[skill,patch,creds,file("settings.yaml","model: example")]);
 expect(selected.included.map(item=>item.path)).toEqual([".dsh/skills/review/SKILL.md",".dsh/cordis.patch.yml",".dsh/settings.yaml"]);
 expect(selected.excluded).toHaveLength(1);expect(creds.arrayBuffer).not.toHaveBeenCalled();
});
it("preserves nested folder paths and rejects credential files before reading",()=>{
 const skill=file("SKILL.md","instructions","opencode/skills/tool/SKILL.md");
 const key=file("private.key","private","opencode/skills/tool/private.key");
 const selected=selectLocalFiles("opencode",[skill,key]);
 expect(selected.included.map(item=>item.path)).toEqual([".config/opencode/skills/tool/SKILL.md"]);
 expect(selected.excluded).toHaveLength(1);expect(key.arrayBuffer).not.toHaveBeenCalled();
});
it("imports inline and selects the saved setup for Capsule creation",async()=>{
 const project={id:"p",name:"Project",createdAt:timestamp,updatedAt:timestamp,resourceVersion:1,harnessImages:[{name:"opencode",imageReference:"local:dev"}]};
 const capsule={id:"c",projectId:"p",timelineId:"t",name:"work",state:"Creating" as const,desiredState:"Ready" as const,restoreComplete:true,createdAt:timestamp,updatedAt:timestamp,resourceVersion:1};
 const setups=vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});
 vi.spyOn(api,"projectHarnessSetup").mockResolvedValue({setup:""});vi.spyOn(api,"projectConnection").mockResolvedValue({connectionId:""});vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:false});
 vi.spyOn(api,"previewHarnessSetup").mockResolvedValue({bundle,issues:[],digest:"digest"});
 vi.spyOn(api,"importHarnessSetup").mockImplementation(async()=>{setups.mockResolvedValue({items:[saved]});return saved;});
 const create=vi.spyOn(api,"createCapsule").mockResolvedValue(capsule);vi.spyOn(api,"capsule").mockResolvedValue(capsule);vi.spyOn(api,"runs").mockResolvedValue({items:[]});
 show(<NativeLauncher project={project} harness="opencode"/>);
 await userEvent.click(await screen.findByRole("button",{name:"Import my setup"}));
 expect(screen.getByRole("heading",{name:"Import your setup"})).toBeInTheDocument();
 await userEvent.upload(screen.getByLabelText("Choose configuration files"),file("opencode.json",bundle.files[0]!.content));
 await userEvent.click(screen.getByRole("button",{name:"Review import"}));
 await userEvent.click(await screen.findByRole("button",{name:"Save setup"}));
 expect(await screen.findByText(/Using My OpenCode setup/)).toBeInTheDocument();
 await userEvent.click(screen.getByRole("button",{name:"Start OpenCode"}));
 await waitFor(()=>expect(create).toHaveBeenCalledWith("p",expect.any(String),"opencode",undefined,"setup"));
});

it("skips dependency directories before enumeration and authentication files before access", async () => {
 const {scanSetupDirectory} = await import("../import/localSetup");
 const dependencyEntries = vi.fn(async function* () { throw new Error("must not enumerate dependencies"); });
 const authRead = vi.fn();
 const configRead = vi.fn().mockResolvedValue(file("opencode.json", "{}"));
 const root = {kind: "directory" as const, name: "opencode", async *values() {
   yield {kind: "directory" as const, name: "node_modules", values: dependencyEntries};
   yield {kind: "file" as const, name: "auth.json", getFile: authRead};
   yield {kind: "file" as const, name: "opencode.json", getFile: configRead};
 }};
 const result = await scanSetupDirectory("opencode", root);
 expect(dependencyEntries).not.toHaveBeenCalled();expect(authRead).not.toHaveBeenCalled();
 expect(result.included.map(item => item.path)).toEqual([".config/opencode/opencode.json"]);
 expect(result.excluded).toHaveLength(2);
});

it("maps additional skill folders consistently in both browser pickers", async () => {
 const {scanSetupDirectory} = await import("../import/localSetup");
 const content = "---\nname: review\ndescription: Review code\n---\nReview code.";
 const selected = selectLocalFiles("opencode", [file("SKILL.md",content,"skills/review/SKILL.md")], "skills/");
 const root = {kind:"directory" as const,name:"skills",async *values() {
   yield {kind:"directory" as const,name:"review",async *values() {
     yield {kind:"file" as const,name:"SKILL.md",getFile:async()=>file("SKILL.md",content)};
   }};
 }};
 const scanned = await scanSetupDirectory("opencode", root, "skills/");
 expect(scanned.included.map(item=>item.path)).toEqual(selected.included.map(item=>item.path));
 expect(scanned.included[0]?.path).toBe(".config/opencode/skills/review/SKILL.md");
});

it("lays out the browser import as steps and offers the one-step CLI import", async () => {
 const writeText = vi.fn().mockResolvedValue(undefined);
 vi.stubGlobal("navigator", {...navigator, clipboard: {writeText}});
 try {
   show(<HarnessSetupImport setups={[]}/>);
   expect(screen.getAllByRole("heading",{level:3}).map(heading=>heading.textContent)).toEqual(["Choose your OpenCode folder","Review and save"]);
   // The path is a hint for the file picker, not a selection.
   const hint = screen.getByText(/A web page cannot open a path by itself/);
   expect(hint).toHaveTextContent("Command-Shift-G in the picker and paste ~/.config/opencode");
   // Nothing to review until something is chosen.
   expect(screen.getByRole("button",{name:"Review import"})).toBeDisabled();
   expect(screen.getByText(/Choose a folder or files first/)).toBeInTheDocument();
   // The test page is served from localhost, so the command needs no server flag.
   expect(screen.getByText("meridian harness import opencode")).toBeInTheDocument();
   expect(screen.getByText(/Fastest: import from a terminal/).closest("p")).not.toHaveTextContent("~/.claude.json");
   await userEvent.click(screen.getByRole("button",{name:"Copy path: ~/.config/opencode"}));
   expect(writeText).toHaveBeenCalledWith("~/.config/opencode");
   expect(await screen.findByRole("status")).toHaveTextContent("Copied");
   await userEvent.selectOptions(screen.getByLabelText("Harness"), "codex");
   expect(screen.getByText(/A web page cannot open a path by itself/)).toHaveTextContent("~/.codex");
   await userEvent.click(screen.getByRole("button",{name:"Copy command: meridian harness import codex"}));
   expect(writeText).toHaveBeenLastCalledWith("meridian harness import codex");
   expect(screen.getByText(/Fastest: import from a terminal/).closest("p")).toHaveTextContent("plus ~/.agents/skills");
   cleanup();
   // Inside the launcher the panel stays to the browser steps.
   show(<HarnessSetupImport setups={[]} onCancel={() => undefined} initialHarness="claude"/>);
   expect(screen.getByText(/A web page cannot open a path by itself/)).toHaveTextContent("~/.claude");
   expect(screen.queryByText(/Fastest: import from a terminal/)).not.toBeInTheDocument();
 } finally {vi.unstubAllGlobals();}
});

it("says when the browser refuses clipboard access", async () => {
 vi.stubGlobal("navigator", {...navigator, clipboard: {writeText: vi.fn().mockRejectedValue(new Error("denied"))}});
 try {
   show(<HarnessSetupImport setups={[]}/>);
   await userEvent.click(screen.getByRole("button",{name:"Copy path: ~/.config/opencode"}));
   expect(await screen.findByRole("status")).toHaveTextContent("did not allow clipboard access");
 } finally {vi.unstubAllGlobals();}
});

it("uses read-only directory access without uploading until review", async () => {
 const picker = vi.fn().mockResolvedValue({kind:"directory",name:"opencode",async *values() {
   yield {kind:"file",name:"opencode.json",getFile:async()=>file("opencode.json","{}")};
 }});
 vi.stubGlobal("showDirectoryPicker",picker);
 try {
   const preview = vi.spyOn(api,"previewHarnessSetup").mockResolvedValue({bundle,issues:[],digest:"digest"});
   show(<HarnessSetupImport setups={[]}/>);
   await userEvent.click(screen.getByRole("button",{name:"Choose folder"}));
   expect(await screen.findByText("1 files selected")).toBeInTheDocument();
   expect(picker).toHaveBeenCalledWith({mode:"read"});expect(preview).not.toHaveBeenCalled();
   await userEvent.click(screen.getByRole("button",{name:"Review import"}));
   await waitFor(()=>expect(preview).toHaveBeenCalled());
 } finally {vi.unstubAllGlobals();}
});
