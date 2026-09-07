import { useEffect, useRef, useState } from "react";

import type { Project } from "../api/generated/types.gen";
import { ProjectEnvironment } from "./ProjectEnvironment";
import { NativeLauncher } from "./NativeLauncher";
import { StructuredLauncher } from "./StructuredLauncher";
import { CloseIcon, TerminalIcon, ThreadIcon } from "./ui/Icons";

export function NewWorkspaceDialog({
  open,
  project,
  onClose,
}: {
  open: boolean;
  project?: Project;
  onClose: () => void;
}) {
  const [mode, setMode] = useState<"native" | "structured">("native");
  const closeRef = useRef<HTMLButtonElement>(null);

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
              {project ? `Start work in ${project.name}` : "Start work"}
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
        <div className="segmented-control" role="tablist" aria-label="Workspace type">
          <button
            type="button"
            role="tab"
            aria-selected={mode === "native"}
            className={mode === "native" ? "active" : undefined}
            onClick={() => setMode("native")}
          >
            <TerminalIcon />
            Native harness
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={mode === "structured"}
            className={mode === "structured" ? "active" : undefined}
            onClick={() => setMode("structured")}
          >
            <ThreadIcon />
            Structured Thread
          </button>
        </div>
        <div className="dialog-body">
          {!project ? (
            <div className="empty-inline">Select a Project before starting work.</div>
          ) : mode === "native" ? (
            <NativeLauncher key={project.id} project={project} />
          ) : (
            <StructuredLauncher key={project.id} project={project} />
          )}
          {project && <ProjectEnvironment key={project.id + "-environment"} projectId={project.id} />}
        </div>
      </section>
    </div>
  );
}
