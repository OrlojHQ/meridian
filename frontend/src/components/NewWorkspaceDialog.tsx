import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { HarnessImage, Project } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { harnessName } from "../shell/capsuleActivity";
import { ProjectEnvironment } from "./ProjectEnvironment";
import { NativeLauncher } from "./NativeLauncher";
import { StructuredLauncher } from "./StructuredLauncher";
import { HarnessMark } from "./ui/HarnessMark";
import { CloseIcon, PlusIcon, TerminalIcon, ThreadIcon } from "./ui/Icons";

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

function AgentPicker({
  project,
  selected,
  onSelect,
  onApplied,
}: {
  project: Project;
  selected: string;
  onSelect: (harness: string) => void;
  onApplied: (project: Project, harness: string) => void;
}) {
  const queryClient = useQueryClient();
  const capabilities = useQuery(queries.capabilities());
  const [adding, setAdding] = useState(false);
  const packs = project.harnessImages ?? [];
  const available = (capabilities.data?.harnessImages ?? []).filter(
    (item) => !packs.some((pack) => pack.name === item.name),
  );
  const apply = useMutation({
    mutationFn: (pack: HarnessImage) => api.applyProjectHarness(project.id, pack),
    onSuccess: async (updated, pack) => {
      setAdding(false);
      onApplied(updated, pack.name);
      await queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
  });
  const showCatalog = packs.length === 0 || adding;

  return (
    <fieldset className="agent-picker">
      <legend>Agent</legend>
      {packs.length > 0 && (
        <div className="agent-options">
          {packs.map((pack) => (
            <label
              key={pack.name}
              className={`agent-option ${selected === pack.name ? "selected" : ""}`}
            >
              <input
                type="radio"
                name="agent"
                value={pack.name}
                checked={selected === pack.name}
                onChange={() => onSelect(pack.name)}
              />
              <HarnessMark harness={pack.name} size="md" />
              <span>{harnessName(pack.name)}</span>
            </label>
          ))}
          {available.length > 0 && (
            <button
              type="button"
              className="agent-add"
              aria-expanded={adding}
              onClick={() => setAdding((current) => !current)}
            >
              <PlusIcon />
              Add agent
            </button>
          )}
        </div>
      )}
      {showCatalog && (
        <div className="agent-catalog">
          <p>
            {packs.length === 0
              ? `${project.name} has no agents yet. Add one to start working.`
              : `Add another agent to ${project.name}.`}
          </p>
          {capabilities.isPending ? (
            <p role="status">Loading installed agents…</p>
          ) : capabilities.isError ? (
            <p className="inline-error" role="alert">
              {normalizeAPIError(capabilities.error).message}
            </p>
          ) : available.length === 0 ? (
            <p className="notice">
              This Meridian installation has no agent images. Configure harness
              packs for meridiand before starting work.
            </p>
          ) : (
            <ul className="agent-catalog-list">
              {available.map((pack) => (
                <li key={pack.name}>
                  <button
                    type="button"
                    disabled={apply.isPending}
                    onClick={() => apply.mutate(pack)}
                  >
                    <HarnessMark harness={pack.name} size="md" />
                    {apply.isPending && apply.variables?.name === pack.name
                      ? `Adding ${harnessName(pack.name)}…`
                      : `Add ${harnessName(pack.name)}`}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {apply.isError && (
            <p className="inline-error" role="alert">
              {normalizeAPIError(apply.error).message}
            </p>
          )}
        </div>
      )}
    </fieldset>
  );
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
  const current = applied && applied.id === project?.id ? applied : project;

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
  const harness = packs.find((pack) => pack.name === preferred)?.name ?? packs[0]?.name ?? "";
  const structured = capabilities.data?.structured === true;
  const activeMode = structured ? mode : "terminal";
  const select = (value: string) => {
    if (!current) return;
    saveHarness(current.id, value);
    setChoice({ projectId: current.id, harness: value });
  };

  return (
    <div className="dialog-backdrop" onMouseDown={onClose}>
      <section
        className="launch-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-workspace-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="dialog-header">
          <div>
            <p className="eyebrow">New Capsule</p>
            <h1 id="new-workspace-title">
              {current ? `Start work in ${current.name}` : "Start work"}
            </h1>
          </div>
          <button
            ref={closeRef}
            type="button"
            className="icon-button"
            aria-label="Close new workspace"
            onClick={onClose}
          >
            <CloseIcon />
          </button>
        </header>
        <div className="dialog-body">
          {!current ? (
            <div className="empty-inline">Select a Project before starting work.</div>
          ) : (
            <>
              <AgentPicker
                project={current}
                selected={harness}
                onSelect={select}
                onApplied={(updated, value) => {
                  setApplied(updated);
                  saveHarness(updated.id, value);
                  setChoice({ projectId: updated.id, harness: value });
                }}
              />
              {harness && structured && (
                <div className="segmented-control" role="tablist" aria-label="How to start">
                  <button
                    type="button"
                    role="tab"
                    aria-selected={activeMode === "terminal"}
                    className={activeMode === "terminal" ? "active" : undefined}
                    onClick={() => setMode("terminal")}
                  >
                    <TerminalIcon />
                    Open terminal
                  </button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={activeMode === "task"}
                    className={activeMode === "task" ? "active" : undefined}
                    onClick={() => setMode("task")}
                  >
                    <ThreadIcon />
                    Give it a task
                  </button>
                </div>
              )}
              {harness &&
                (activeMode === "terminal" ? (
                  <NativeLauncher
                    key={`${current.id}-${harness}`}
                    project={current}
                    harness={harness}
                  />
                ) : (
                  <StructuredLauncher
                    key={`${current.id}-${harness}`}
                    project={current}
                    harness={harness}
                  />
                ))}
              <ProjectEnvironment key={current.id + "-environment"} projectId={current.id} />
            </>
          )}
        </div>
      </section>
    </div>
  );
}
