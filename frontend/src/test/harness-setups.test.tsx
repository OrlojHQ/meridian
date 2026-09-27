import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { HarnessSetups } from "../components/HarnessSetups";
import { NativeLauncher } from "../components/NativeLauncher";

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
