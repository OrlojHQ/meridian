import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { HarnessImage, Project } from "../api/generated/types.gen";
import { projectHarnessImage } from "../api/harnessPacks";
import { isGitHubRepositoryInput } from "./repositoryUrl";
import { CloseIcon } from "./ui/Icons";

const harnessPreferenceKey = "meridian.newProjectHarness";

function rememberedHarness() {
  try {
    return localStorage.getItem(harnessPreferenceKey) ?? "";
  } catch {
    return "";
  }
}

function rememberHarness(harness: string) {
  try {
    localStorage.setItem(harnessPreferenceKey, harness);
  } catch {
    // The preference is a convenience; creating a Project does not depend on it.
  }
}

// The installation orders its catalog (official agents first, mock last), so
// without a remembered choice the first real agent wins.
export function initialHarness(harnessImages: HarnessImage[]) {
  const remembered = rememberedHarness();
  if (harnessImages.some((harness) => harness.name === remembered)) {
    return remembered;
  }
  return (
    harnessImages.find((harness) => harness.name !== "mock") ?? harnessImages[0]
  )?.name;
}

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
  const [repositoryUrl, setRepositoryUrl] = useState("");
  const [privateRepo, setPrivateRepo] = useState(false);
  const [token, setToken] = useState("");
  const github = isGitHubRepositoryInput(repositoryUrl);
  const mutation = useMutation({
    mutationFn: async ({
      name,
      repositoryUrl: repository,
      harnessName,
      token: cloneToken,
    }: {
      name: string;
      repositoryUrl?: string;
      harnessName?: string;
      token?: string;
    }) => {
      const harness = harnessImages.find((item) => item.name === harnessName);
      let gitSecretName: string | undefined;
      if (cloneToken) {
        gitSecretName = `git_${crypto.randomUUID().replaceAll("-", "")}`;
        const secret = await api.putGitHTTPSSecret(gitSecretName, cloneToken);
        try {
          return await api.createProject({
            name,
            ...(repository ? { repositoryUrl: repository } : {}),
            gitSecretName,
            ...(harness ? { harnessImages: [projectHarnessImage(harness)] } : {}),
          });
        } catch (error) {
          await api.deleteSecret(gitSecretName, secret.resourceVersion).catch(() => undefined);
          throw error;
        }
      }
      return api.createProject({
        name,
        ...(repository ? { repositoryUrl: repository } : {}),
        ...(harness ? { harnessImages: [projectHarnessImage(harness)] } : {}),
      });
    },
    onSuccess: async (project, { harnessName }) => {
      const chosen = harnessImages.find((harness) => harness.name === harnessName);
      if (chosen) rememberHarness(chosen.name);
      await queryClient.invalidateQueries({ queryKey: ["projects"] });
      onCreated?.(project);
      onClose();
    },
  });

  const preselected = useMemo(
    () => (open ? initialHarness(harnessImages) : undefined),
    [harnessImages, open],
  );

  useEffect(() => {
    if (open) return;
    setRepositoryUrl("");
    setPrivateRepo(false);
    setToken("");
  }, [open]);

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
    const cloneToken = privateRepo ? token.trim() : "";
    mutation.mutate({
      name: String(data.get("name") ?? ""),
      repositoryUrl: repositoryUrl.trim() || undefined,
      harnessName: String(data.get("harness") ?? "") || undefined,
      ...(cloneToken ? { token: cloneToken } : {}),
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
                value={repositoryUrl}
                maxLength={2048}
                disabled={mutation.isPending}
                placeholder="owner/repo or https://github.com/org/repository"
                onChange={(event) => {
                  const next = event.target.value;
                  setRepositoryUrl(next);
                  if (!isGitHubRepositoryInput(next)) {
                    setPrivateRepo(false);
                    setToken("");
                  }
                }}
              />
            </label>
            {github && (
              <p className="field-hint">Stored as HTTPS.</p>
            )}
            {github && (
              <label className="checkbox-label" htmlFor="project-private">
                <input
                  id="project-private"
                  type="checkbox"
                  checked={privateRepo}
                  disabled={mutation.isPending}
                  onChange={(event) => {
                    setPrivateRepo(event.target.checked);
                    if (!event.target.checked) setToken("");
                  }}
                />
                Private repository
              </label>
            )}
            {github && privateRepo && (
              <label htmlFor="project-git-token">
                GitHub token
                <input
                  id="project-git-token"
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  required
                  maxLength={4096}
                  value={token}
                  disabled={mutation.isPending}
                  onChange={(event) => setToken(event.target.value)}
                />
              </label>
            )}
            {github && privateRepo && (
              <p className="field-hint">Used only to clone. Stored encrypted.</p>
            )}
            {harnessImages.length > 0 ? (
              <label htmlFor="project-harness">
                Initial harness pack
                <select
                  id="project-harness"
                  name="harness"
                  defaultValue={preselected}
                  disabled={mutation.isPending}
                >
                  {harnessImages.map((harness) => (
                    <option key={harness.name} value={harness.name}>
                      {harness.name === "mock"
                        ? "mock (test harness, no agent)"
                        : harness.name}
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
