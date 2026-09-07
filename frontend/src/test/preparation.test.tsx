import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { PreparationProgress } from "../components/PreparationProgress";
import { ProjectEnvironment } from "../components/ProjectEnvironment";

const timestamp="2026-09-07T12:00:00Z";
function show(ui:React.ReactNode){return render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}})}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>);}
afterEach(()=>{cleanup();vi.restoreAllMocks();});
it("retries preparation in the existing Capsule",async()=>{
 const capsule={id:"same-capsule",projectId:"project",timelineId:"timeline",name:"work",state:"Failed" as const,desiredState:"Ready" as const,restoreComplete:true,resourceVersion:4,createdAt:timestamp,updatedAt:timestamp,failure:"Project setup failed",preparation:{stage:"preparing" as const,reused:false,updatedAt:timestamp}};
 const retry=vi.spyOn(api,"lifecycle").mockResolvedValue({...capsule,state:"Preparing"});
 show(<PreparationProgress capsule={capsule}/>);
 await userEvent.click(screen.getByRole("button",{name:"Retry preparation"}));
 await waitFor(()=>expect(retry).toHaveBeenCalledWith("retry","same-capsule",4));
});
it("explains a reused environment",()=>{
 show(<PreparationProgress capsule={{id:"c",projectId:"p",timelineId:"t",name:"work",state:"Ready",desiredState:"Ready",restoreComplete:true,resourceVersion:2,createdAt:timestamp,updatedAt:timestamp,preparation:{stage:"ready",reused:true,updatedAt:timestamp}}}/>);
 expect(screen.getByText("Reused your prepared project environment.")).toBeInTheDocument();
 expect(screen.queryByRole("button",{name:"Retry preparation"})).not.toBeInTheDocument();
});
it("requests an explicit rebuild for the next launch",async()=>{
 const environment={enabled:true,generation:0,setup:[],imageReference:"example:approved",resourceVersion:3};
 vi.spyOn(api,"projectEnvironment").mockResolvedValue(environment);
 const update=vi.spyOn(api,"updateProjectEnvironment").mockResolvedValue({...environment,generation:1,resourceVersion:4});
 show(<ProjectEnvironment projectId="project"/>);
 await userEvent.click(screen.getByText("Project environment"));
 await userEvent.click(await screen.findByRole("button",{name:"Rebuild on next launch"}));
 await waitFor(()=>expect(update).toHaveBeenCalledWith("project",{rebuild:true,expectedResourceVersion:3}));
});
