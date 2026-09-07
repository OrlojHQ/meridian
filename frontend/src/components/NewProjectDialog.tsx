import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useRef } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { HarnessImage, Project } from "../api/generated/types.gen";
import { CloseIcon } from "./ui/Icons";

export function NewProjectDialog({
  open,
  harnessImages,
  onClose,
  onCreated,
}: {
  open: boolean;
  harnessImages: HarnessImage[];
  onClose: () => void;
  onCreated?: (project: Project) => void;
}) {
  const queryClient = useQueryClient();
  const closeRef = useRef<HTMLButtonElement>(null);
  const mutation = useMutation({
    mutationFn: ({
      name,
      repositoryUrl,
      harnessName,
    }: {
      name: string;
      repositoryUrl?: string;
      harnessName?: string;
    }) => {
      const harness = harnessImages.find((item) => item.name === harnessName);
      return api.createProject({
        name,
        ...(repositoryUrl ? { repositoryUrl } : {}),
        ...(harness ? { harnessImages: [harness] } : {}),
      });
    },
    onSuccess: async (project) => {
      await queryClient.invalidateQueries({ queryKey: ["projects"] });
      onCreated?.(project);
      onClose();
    },
  });

  useEffect(() => {
    if (!open) return;
    closeRef.current?.focus();
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !mutation.isPending) onClose();
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [mutation.isPending, onClose, open]);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    mutation.mutate({
      name: String(data.get("name") ?? ""),
      repositoryUrl: String(data.get("repositoryUrl") ?? "") || undefined,
      harnessName: String(data.get("harness") ?? "") || undefined,
    });
  }

  if (!open) return null;

  return (
    <div
      className="dialog-backdrop"
      onMouseDown={() => {
        if (!mutation.isPending) onClose();
      }}
    >
      <section
        className="launch-dialog project-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-project-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="dialog-header">
          <div>
            <p className="eyebrow">New project</p>
            <h1 id="new-project-title">Create a Project</h1>
          </div>
          <button
            ref={closeRef}
            type="button"
            className="icon-button"
            aria-label="Close new project"
            disabled={mutation.isPending}
            onClick={onClose}
          >
            <CloseIcon />
          </button>
        </header>
        <div className="dialog-body">
          <form className="launcher-form" onSubmit={submit}>
            <label htmlFor="project-name">
              Project name
              <input
                id="project-name"
                name="name"
                required
                maxLength={128}
                disabled={mutation.isPending}
                placeholder="Meridian"
              />
            </label>
            <label htmlFor="project-repository">
              Repository URL <span className="optional-label">Optional</span>
              <input
                id="project-repository"
                name="repositoryUrl"
                maxLength={2048}
                disabled={mutation.isPending}
                placeholder="https://github.com/org/repository.git"
              />
            </label>
            {harnessImages.length > 0 ? (
              <label htmlFor="project-harness">
                Initial harness pack
                <select
                  id="project-harness"
                  name="harness"
                  defaultValue={harnessImages[0]?.name}
                  disabled={mutation.isPending}
                >
                  {harnessImages.map((harness) => (
                    <option key={harness.name} value={harness.name}>
                      {harness.name}
                    </option>
                  ))}
                </select>
              </label>
            ) : (
              <p className="notice">
                No installation harness packs are available. The Project can be
                created, but Capsules cannot launch until an operator applies one.
              </p>
            )}
            <button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? "Creating…" : "Create Project"}
            </button>
          </form>
          {mutation.isError && (
            <p className="inline-error" role="alert">
              {normalizeAPIError(mutation.error).message}
            </p>
          )}
        </div>
      </section>
    </div>
  );
}
