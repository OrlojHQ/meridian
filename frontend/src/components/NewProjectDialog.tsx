import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { HarnessImage, IsolationClass, Project } from "../api/generated/types.gen";
import { projectHarnessImage } from "../api/harnessPacks";
import { githubConnectionQuery } from "./GitHubConnect";
import { isGitHubRepositoryInput } from "./repositoryUrl";
import { Button } from "./ui/Button";
import { Dialog } from "./ui/Dialog";
import { Input, Select } from "./ui/Field";

const harnessPreferenceKey = "meridian.newProjectHarness";

// The select always offers every class; the server stays authoritative and
// refuses a requirement the Station cannot serve (ADR 0050). Options the
// verified class cannot back are disabled, never hidden, so an operator
// sees what the Station could offer with a stronger provider.
const isolationOptions: { value: IsolationClass | ""; label: string; rank: number }[] = [
  { value: "", label: "No requirement", rank: 0 },
  { value: "container", label: "Container", rank: 1 },
  { value: "sandbox", label: "Sandbox (gVisor-class)", rank: 2 },
  { value: "microvm", label: "MicroVM", rank: 3 },
];

const isolationRanks: Record<IsolationClass, number> = {
  container: 1,
  sandbox: 2,
  microvm: 3,
};

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

// Without a remembered choice, the first installation pack wins.
export function initialHarness(harnessImages: HarnessImage[]) {
  const remembered = rememberedHarness();
  if (harnessImages.some((harness) => harness.name === remembered)) {
    return remembered;
  }
  return harnessImages[0]?.name;
}

export function NewProjectDialog({
  open,
  harnessImages,
  verifiedIsolation,
  onClose,
  onCreated,
}: {
  open: boolean;
  harnessImages: HarnessImage[];
  verifiedIsolation?: IsolationClass;
  onClose: () => void;
  onCreated?: (project: Project) => void;
}) {
  const queryClient = useQueryClient();
  const closeRef = useRef<HTMLButtonElement>(null);
  const [repositoryUrl, setRepositoryUrl] = useState("");
  const [repoQuery, setRepoQuery] = useState("");
  const [repoFocus, setRepoFocus] = useState(false);
  const [useToken, setUseToken] = useState(false);
  const [privateRepo, setPrivateRepo] = useState(false);
  const [token, setToken] = useState("");
  const [requiredIsolation, setRequiredIsolation] = useState<IsolationClass | "">("");
  const connection = useQuery({ ...githubConnectionQuery, enabled: open, retry: false });
  const githubConnected = connection.data?.status === "active";
  const repos = useQuery({
    queryKey: ["github-repos", repoQuery],
    queryFn: ({ signal }) => api.searchGitHubRepos(repoQuery, signal),
    enabled: open && githubConnected && repoFocus,
    retry: false,
  });
  const github = isGitHubRepositoryInput(repositoryUrl);
  const verifiedRank = verifiedIsolation ? isolationRanks[verifiedIsolation] : undefined;
  const mutation = useMutation({
    mutationFn: async ({
      name,
      repositoryUrl: repository,
      harnessName,
      token: cloneToken,
      requiredIsolation: requirement,
    }: {
      name: string;
      repositoryUrl?: string;
      harnessName?: string;
      token?: string;
      requiredIsolation?: IsolationClass;
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
            ...(requirement ? { requiredIsolation: requirement } : {}),
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
        ...(requirement ? { requiredIsolation: requirement } : {}),
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
    setRepoQuery("");
    setRepoFocus(false);
    setUseToken(false);
    setPrivateRepo(false);
    setToken("");
    setRequiredIsolation("");
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const timer = window.setTimeout(() => setRepoQuery(repositoryUrl.trim()), 200);
    return () => window.clearTimeout(timer);
  }, [open, repositoryUrl]);

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
    const cloneToken = useToken && privateRepo ? token.trim() : "";
    mutation.mutate({
      name: String(data.get("name") ?? ""),
      repositoryUrl: repositoryUrl.trim() || undefined,
      harnessName: String(data.get("harness") ?? "") || undefined,
      ...(cloneToken ? { token: cloneToken } : {}),
      ...(requiredIsolation ? { requiredIsolation } : {}),
    });
  }

  if (!open) return null;

  return (
    <Dialog
      labelledBy="new-project-title"
      eyebrow="New project"
      title="Create a Project"
      closeLabel="Close new project"
      closeRef={closeRef}
      busy={mutation.isPending}
      onClose={onClose}
      className="project-dialog"
    >
      <form className="launcher-form" onSubmit={submit}>
        <label htmlFor="project-name">
          Project name
          <Input
            id="project-name"
            name="name"
            required
            maxLength={128}
            disabled={mutation.isPending}
            placeholder="Meridian"
          />
        </label>
        <label htmlFor="project-repository">
          Repository <span className="optional-label">Optional</span>
          <Input
            id="project-repository"
            role="combobox"
            aria-expanded={repoFocus && githubConnected}
            aria-controls="project-repo-results"
            value={repositoryUrl}
            maxLength={2048}
            disabled={mutation.isPending}
            placeholder={githubConnected ? "Search repositories or paste a URL" : "owner/repo or https://github.com/org/repository"}
            autoComplete="off"
            onFocus={() => setRepoFocus(true)}
            onBlur={() => window.setTimeout(() => setRepoFocus(false), 150)}
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
        {repoFocus && githubConnected && (
          <ul id="project-repo-results" className="repo-results" role="listbox">
            {repos.isPending && <li className="repo-empty">Searching repositories…</li>}
            {repos.isError && <li className="repo-empty">{normalizeAPIError(repos.error).message}</li>}
            {repos.data?.items.length === 0 && <li className="repo-empty">No matching repositories.</li>}
            {repos.data?.items.map((repo) => (
              <li key={repo.fullName}>
                <button
                  type="button"
                  role="option"
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => {
                    setRepositoryUrl(repo.url);
                    setRepoFocus(false);
                  }}
                >
                  <span>
                    <strong>{repo.fullName}</strong>
                    {repo.private && <em className="repo-private">Private</em>}
                  </span>
                  {repo.description && <small>{repo.description}</small>}
                </button>
              </li>
            ))}
          </ul>
        )}
        {github && <p className="field-hint">Stored as HTTPS.</p>}
        {githubConnected && github && !useToken && (
          <p className="field-hint">Private repositories use your GitHub connection. No token is stored on the Project.</p>
        )}
        {!githubConnected && github && !useToken && (
          <p className="field-hint">
            <a href="/ui/settings/account#github">Connect GitHub</a> to include private repositories.
          </p>
        )}
        {(github || githubConnected) && !useToken && (
          <button type="button" className="text-button" onClick={() => setUseToken(true)}>
            Use a token instead
          </button>
        )}
        {useToken && github && (
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
        {useToken && github && privateRepo && (
          <label htmlFor="project-git-token">
            GitHub token
            <Input
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
            <Select
              id="project-harness"
              name="harness"
              defaultValue={preselected}
              disabled={mutation.isPending}
            >
              {harnessImages.map((harness) => (
                <option key={harness.name} value={harness.name}>
                  {harness.name}
                </option>
              ))}
            </Select>
          </label>
        ) : (
          <p className="notice">
            No installation harness packs are available. The Project can be
            created, but Capsules cannot launch until an operator applies one.
          </p>
        )}
        <label htmlFor="project-isolation">
          Isolation
          <Select
            id="project-isolation"
            value={requiredIsolation}
            disabled={mutation.isPending}
            onChange={(event) => setRequiredIsolation(event.target.value as IsolationClass | "")}
          >
            {isolationOptions.map((option) => (
              <option
                key={option.value || "none"}
                value={option.value}
                disabled={verifiedRank !== undefined && option.rank > verifiedRank}
              >
                {option.label}
              </option>
            ))}
          </Select>
        </label>
        {verifiedIsolation && (
          <p className="field-hint">
            This Station provides {verifiedIsolation} isolation.
          </p>
        )}
        <Button type="submit" variant="primary" disabled={mutation.isPending}>
          {mutation.isPending ? "Creating…" : "Create Project"}
        </Button>
      </form>
      {mutation.isError && (
        <p className="inline-error" role="alert">
          {normalizeAPIError(mutation.error).message}
        </p>
      )}
    </Dialog>
  );
}
