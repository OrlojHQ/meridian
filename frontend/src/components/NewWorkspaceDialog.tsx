import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import type { Project } from "../api/generated/types.gen";
import { packMayRunNative, packMayRunStructured } from "../api/harnessPacks";
import { queries } from "../api/queries";
import { ProjectEnvironment } from "./ProjectEnvironment";
import { ProjectHarnesses } from "./ProjectHarnesses";
import { NativeLauncher } from "./NativeLauncher";
import { StructuredLauncher } from "./StructuredLauncher";
import { Dialog } from "./ui/Dialog";
import { TerminalIcon, ThreadIcon } from "./ui/Icons";
import { Tabs } from "./ui/Tabs";

const preferenceKey = (projectId: string) => `meridian.harness.${projectId}`;

function savedHarness(projectId: string) {
  try {
    return localStorage.getItem(preferenceKey(projectId)) ?? "";
  } catch {
    return "";
  }
}

function saveHarness(projectId: string, harness: string) {
  try {
    localStorage.setItem(preferenceKey(projectId), harness);
  } catch {
    // The preference is a convenience; launching does not depend on it.
  }
}

export function NewWorkspaceDialog({
  open,
  project,
  onClose,
}: {
  open: boolean;
  project?: Project;
  onClose: () => void;
}) {
  const [mode, setMode] = useState<"terminal" | "task">("terminal");
  const [applied, setApplied] = useState<Project>();
  const [choice, setChoice] = useState<{ projectId: string; harness: string }>();
  const closeRef = useRef<HTMLButtonElement>(null);
  const capabilities = useQuery(queries.capabilities());
  const current = applied && applied.id === project?.id && applied.resourceVersion >= project.resourceVersion
    ? applied
    : project;

  useEffect(() => {
    if (!open) return;
    closeRef.current?.focus();
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [onClose, open]);

  if (!open) return null;

  const packs = current?.harnessImages ?? [];
  const preferred =
    current && choice?.projectId === current.id
      ? choice.harness
      : current
        ? savedHarness(current.id)
        : "";
  const pack = packs.find((item) => item.name === preferred) ?? packs[0];
  const harness = pack?.name ?? "";
  // Official packs that declare only native sessions get just the terminal
  // launcher; the daemon would refuse their task before provisioning. Packs
  // that declare only structured sessions get just the task launcher.
  const structured =
    capabilities.data?.structured === true &&
    pack !== undefined &&
    packMayRunStructured(pack, capabilities.data.harnessImages ?? []);
  const native = pack === undefined || packMayRunNative(pack, capabilities.data?.harnessImages ?? []);
  const activeMode = structured && (!native || mode === "task") ? "task" : "terminal";
  const select = (value: string) => {
    if (!current) return;
    saveHarness(current.id, value);
    setChoice({ projectId: current.id, harness: value });
  };

  return (
    <Dialog
      labelledBy="new-workspace-title"
      eyebrow="New Capsule"
      title={current ? `Start work in ${current.name}` : "Start work"}
      closeLabel="Close new workspace"
      closeRef={closeRef}
      onClose={onClose}
    >
          {!current ? (
            <div className="empty-inline">Select a Project before starting work.</div>
          ) : (
            <>
              <ProjectHarnesses
                key={current.id}
                project={current}
                selection={{ value: harness, onChange: select }}
                onApplied={(updated, value) => {
                  setApplied(updated);
                  saveHarness(updated.id, value);
                  setChoice({ projectId: updated.id, harness: value });
                }}
              />
              {harness && structured && native && (
                <Tabs
                  label="How to start"
                  value={activeMode}
                  onChange={(value) => setMode(value === "task" ? "task" : "terminal")}
                  tabs={[
                    { id: "terminal", label: "Open terminal", icon: <TerminalIcon /> },
                    { id: "task", label: "Give it a task", icon: <ThreadIcon /> },
                  ]}
                />
              )}
              {harness &&
                (activeMode === "terminal" ? (
                  <NativeLauncher
                    key={`${current.id}-${harness}`}
                    project={current}
                    harness={harness}
                    onStarted={onClose}
                  />
                ) : (
                  <StructuredLauncher
                    key={`${current.id}-${harness}`}
                    project={current}
                    harness={harness}
                    onStarted={onClose}
                  />
                ))}
              <ProjectEnvironment key={current.id + "-environment"} projectId={current.id} />
            </>
          )}
    </Dialog>
  );
}
