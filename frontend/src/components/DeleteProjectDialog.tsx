import { useQuery, useQueries } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";

import { api } from "../api/client";
import type { Project } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { projectSecretNames } from "../shell/capsuleActivity";

function messageOf(error: unknown) {
  return error instanceof Error ? error.message : "The count could not be loaded";
}

export function DeleteProjectDialog({
  project,
  onCancel,
  onDeleted,
}: {
  project: Project;
  onCancel: () => void;
  onDeleted: (project: Project) => void;
}) {
  const [name, setName] = useState("");
  const [error, setError] = useState<Error>();
  const [pending, setPending] = useState(false);
  const capsules = useQuery(queries.capsules(project.id));
  const items = capsules.data?.items ?? [];
  const threads = useQueries({
    queries: items.map((capsule) => queries.threads(capsule.id)),
  });
  const threadsReady = items.length === 0 || threads.every((query) => query.isSuccess);
  const threadError = threads.find((query) => query.isError)?.error;
  const capsuleCount = items.filter((capsule) => capsule.state !== "Deleted").length;
  const threadCount = threads.reduce((sum, query) => sum + (query.data?.items.length ?? 0), 0);
  const secretCount = projectSecretNames(project).length;
  const countsReady = capsules.isSuccess && threadsReady;
  const confirmed = name === project.name;
  const retry = project.state === "failed";

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onCancel();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onCancel]);

  async function submit() {
    if (!confirmed || !countsReady || pending) return;
    setPending(true);
    setError(undefined);
    try {
      onDeleted(await api.deleteProject(project));
    } catch (cause) {
      setError(cause instanceof Error ? cause : new Error("Project could not be deleted"));
      setPending(false);
    }
  }

  return createPortal(
    <section
      className="confirm-dialog"
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="delete-project-title"
    >
      <h2 id="delete-project-title">
        {retry ? `Retry deleting ${project.name}?` : `Delete ${project.name}?`}
      </h2>
      <p>This permanently removes:</p>
      <ul>
        <li>{countsReady ? capsuleCount : "…"} Capsules</li>
        <li>{countsReady ? threadCount : "…"} threads</li>
        <li>{secretCount} secrets</li>
      </ul>
      <p>Transcripts and artifacts are deleted with the project and are not kept.</p>
      {retry && (
        <p className="notice" role="status">
          Deletion stopped{project.failure ? `: ${project.failure}` : ""}. Retrying resumes cleanup.
        </p>
      )}
      {capsules.isError && <p role="alert">{messageOf(capsules.error)}</p>}
      {threadError && <p role="alert">{messageOf(threadError)}</p>}
      {error && <p role="alert">{error.message}</p>}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <label>
          Type <strong>{project.name}</strong> to confirm
          <input
            aria-label="Project name"
            value={name}
            autoComplete="off"
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        <div className="actions">
          <button type="submit" className="danger" disabled={!confirmed || !countsReady || pending}>
            {retry ? "Retry delete" : "Delete project"}
          </button>
          <button type="button" className="secondary" autoFocus onClick={onCancel}>
            Keep project
          </button>
        </div>
      </form>
    </section>,
    document.body,
  );
}
