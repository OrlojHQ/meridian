import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import { queries } from "../api/queries";
import type { Profile, ProviderConnection } from "../api/generated/types.gen";
import { harnessName } from "../shell/capsuleActivity";
import { desktopBridge } from "../shell/desktop";
import { connectionSupports } from "./ProviderConnections";
import { HarnessMark } from "./ui/HarnessMark";

// Workstation profiles (ADR 0049). The page never scans the laptop: imports
// come from `meridian import`, the desktop app's own import window, or the
// folder picker in Harness settings.

export const profileQuery = { queryKey: ["profile"], queryFn: () => api.profile() };

const hooksNote = "Hooks, plugins, and MCP servers are code that runs inside the Capsule.";

function isLoopback(host: string) {
  return host === "127.0.0.1" || host === "localhost" || host === "[::1]" || host === "::1";
}

function list(names: string[]) {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

// What the import step offers depends on where the page runs. Inside the
// desktop app it can only ask the app to open its own window, and it is
// never told what the app found. In a browser it points at the CLI and the
// folder picker.
export function ImportThisComputer({ profile }: { profile?: Profile }) {
  const bridge = desktopBridge();
  const [opened, setOpened] = useState<"idle" | "opened" | "failed">("idle");
  const imported = profile?.setups.map((setup) => harnessName(setup.harness)) ?? [];
  return (
    <div className="profile-import">
      {imported.length > 0 && <p className="is-ready">Your profile has {list(imported)}.</p>}
      {bridge?.openImport ? (
        <>
          <button
            type="button"
            className="primary-button"
            onClick={() => {
              void bridge.openImport?.().then(
                (ok) => setOpened(ok ? "opened" : "failed"),
                () => setOpened("failed"),
              );
            }}
          >
            {imported.length > 0 ? "Review this computer's setup again" : "Review this computer's setup"}
          </button>
          {opened === "opened" && <p role="status">Review and save in the window that opened.</p>}
          {opened === "failed" && <p role="alert">The import window could not open. Use the tray menu instead.</p>}
        </>
      ) : (
        <>
          <p>
            Run <code>meridian import</code> on the computer that has your setup. It shows what will be kept and left out
            before anything is saved.
          </p>
          <p>
            Or <Link to="/ui/settings/harnesses#import-setup">choose a folder</Link> for one harness.
          </p>
        </>
      )}
    </div>
  );
}

// The three first-run steps. A step is done only from server state, so the
// page never claims to have found anything on this computer.
export function FirstRun({ openNewProject }: { openNewProject?: () => void }) {
  const profile = useQuery({ ...profileQuery, refetchInterval: (query) => (query.state.data?.setups.length ? false : 5000) });
  const connections = useQuery({ queryKey: ["provider-connections"], queryFn: api.providerConnections });
  const imported = (profile.data?.setups.length ?? 0) > 0;
  const connected = connections.data?.items.some((item) => !item.revoked && !item.needsSignIn) ?? false;
  return (
    <section className="home empty-state first-run" aria-label="Get started">
      <h1>Bring your setup</h1>
      <p>Three steps, and every new Capsule starts the way your computer does.</p>
      <ol className="first-run-steps">
        <li className={imported ? "is-done" : undefined}>
          <h2>Import this computer</h2>
          <ImportThisComputer profile={profile.data} />
        </li>
        <li className={connected ? "is-done" : undefined}>
          <h2>Connect a model</h2>
          {connected ? (
            <p className="is-ready">A provider connection is ready.</p>
          ) : (
            <p>
              Your laptop's sign-ins are not copied. <Link to="/ui/settings/harnesses#provider-connections">Connect a provider</Link>{" "}
              so Threads can use your model.
            </p>
          )}
        </li>
        <li>
          <h2>Create a Project</h2>
          <p>A Project points Meridian at a repository. Every session runs in its own Capsule.</p>
          {openNewProject && (
            <button type="button" className="primary-button" onClick={openNewProject}>
              New project
            </button>
          )}
        </li>
      </ol>
    </section>
  );
}

function DefaultConnection({
  harness,
  profile,
  connections,
  disabled,
  onChange,
}: {
  harness: string;
  profile: Profile;
  connections: ProviderConnection[];
  disabled: boolean;
  onChange: (connectionId: string) => void;
}) {
  const options = connections.filter((item) => !item.revoked && connectionSupports(harness, item));
  const current = profile.defaultConnections[harness] ?? "";
  if (options.length === 0 && !current) return <span>No provider connection</span>;
  return (
    <label className="profile-connection">
      <span className="sr-only">Default connection for {harnessName(harness)}</span>
      <select value={current} disabled={disabled} onChange={(event) => onChange(event.target.value)}>
        <option value="">No default connection</option>
        {options.map((item) => (
          <option key={item.id} value={item.id}>
            {item.name}
          </option>
        ))}
      </select>
    </label>
  );
}

function ProjectOverrides({ projectId, harnesses }: { projectId: string; harnesses: string[] }) {
  const overrides = useQuery({
    queryKey: ["project-profile-overrides", projectId, harnesses],
    queryFn: () =>
      Promise.all(
        harnesses.map(async (harness) => ({
          harness,
          setup: (await api.projectHarnessSetup(projectId, harness)).setup,
          connection: (await api.projectConnection(projectId, harness)).connectionId,
        })),
      ),
  });
  if (overrides.isPending) return <p role="status">Loading…</p>;
  if (overrides.isError) return <p role="alert">{normalizeAPIError(overrides.error).message}</p>;
  return (
    <ul>
      {overrides.data.map((row) => (
        <li key={row.harness}>
          {harnessName(row.harness)}:{" "}
          {row.setup === "clean" ? "no setup (clean)" : row.setup ? "its own setup" : "your profile's setup"}
          {" · "}
          {row.connection ? "a granted connection" : "no connection granted yet"}
        </li>
      ))}
    </ul>
  );
}

// "What Capsules inherit": the profile on one screen, with each Project's
// overrides under it. Editing files stays in the per-harness drawer.
export function WhatCapsulesInherit() {
  const cache = useQueryClient();
  const profile = useQuery(profileQuery);
  const connections = useQuery({ queryKey: ["provider-connections"], queryFn: api.providerConnections });
  const projects = useQuery(queries.projects());
  const mode = useQuery(queries.authenticationMode());
  const [gitName, setGitName] = useState("");
  const [gitEmail, setGitEmail] = useState("");
  useEffect(() => {
    setGitName(profile.data?.gitName ?? "");
    setGitEmail(profile.data?.gitEmail ?? "");
  }, [profile.data?.gitName, profile.data?.gitEmail]);
  const save = useMutation({
    mutationFn: (change: Partial<{ gitName: string; gitEmail: string; defaultConnections: Record<string, string> }>) => {
      const current = profile.data!;
      return api.updateProfile({
        gitName: change.gitName ?? current.gitName,
        gitEmail: change.gitEmail ?? current.gitEmail,
        defaultConnections: change.defaultConnections ?? current.defaultConnections,
        expectedResourceVersion: current.resourceVersion,
      });
    },
    onSuccess: (next) => cache.setQueryData(["profile"], next),
  });
  if (profile.isPending) return <p role="status">Loading your profile…</p>;
  if (profile.isError) return <p role="alert">{normalizeAPIError(profile.error).message}</p>;
  const value = profile.data;
  const harnesses = [...new Set([...value.setups.map((setup) => setup.harness), ...Object.keys(value.defaultConnections)])].sort();
  const shared = mode.data?.mode === "single_user" && !isLoopback(window.location.hostname);
  const dirtyGit = gitName !== value.gitName || gitEmail !== value.gitEmail;
  return (
    <section className="panel what-capsules-inherit" aria-labelledby="inherit-title">
      <h2 id="inherit-title">What Capsules inherit</h2>
      <p className="view-description">
        New Capsules pin this profile when they are created. Existing Capsules keep what they pinned until you update
        them.
      </p>
      {shared && (
        <p className="is-warning">This server is in single-user mode: anyone with its installation token uses this profile.</p>
      )}
      <ImportThisComputer profile={value} />
      {harnesses.length > 0 && (
        <ul className="harness-list profile-harnesses">
          {harnesses.map((harness) => {
            const setup = value.setups.find((item) => item.harness === harness);
            return (
              <li key={harness}>
                <HarnessMark harness={harness} size="lg" />
                <span className="harness-list-copy">
                  <strong>{harnessName(harness)}</strong>
                  <span className="harness-list-status">
                    <span className={setup ? "is-ready" : undefined}>{setup ? `Setup “${setup.name}”` : "No setup imported"}</span>
                  </span>
                </span>
                <DefaultConnection
                  harness={harness}
                  profile={value}
                  connections={connections.data?.items ?? []}
                  disabled={save.isPending}
                  onChange={(connectionId) => {
                    const next = { ...value.defaultConnections };
                    if (connectionId) next[harness] = connectionId;
                    else delete next[harness];
                    save.mutate({ defaultConnections: next });
                  }}
                />
              </li>
            );
          })}
        </ul>
      )}
      <p className="profile-note">{hooksNote}</p>
      <p className="profile-note">
        A default connection is preselected when you launch a harness in a Project. Each Project still asks once before it
        uses your login.
      </p>
      <form
        className="profile-git"
        onSubmit={(event) => {
          event.preventDefault();
          save.mutate({ gitName: gitName.trim(), gitEmail: gitEmail.trim() });
        }}
      >
        <h3>Git commit identity</h3>
        <p>
          Agents inside the Capsule commit as this, and Delivery uses it when a Project has no commit author. Only the
          name and email are kept; SSH keys and the rest of your git config are not.
        </p>
        <label>
          Name
          <input value={gitName} maxLength={320} disabled={save.isPending} onChange={(event) => setGitName(event.target.value)} />
        </label>
        <label>
          Email
          <input
            value={gitEmail}
            maxLength={320}
            type="email"
            disabled={save.isPending}
            onChange={(event) => setGitEmail(event.target.value)}
          />
        </label>
        <button type="submit" disabled={save.isPending || !dirtyGit || (gitName.trim() === "") !== (gitEmail.trim() === "")}>
          Save identity
        </button>
      </form>
      {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
      {(projects.data?.items.length ?? 0) > 0 && (
        <div className="profile-projects">
          <h3>Projects</h3>
          {projects.data!.items.map((project) => (
            <details key={project.id}>
              <summary>
                <Link to={`/ui/projects/${project.id}`}>{project.name}</Link>
                {project.commitAuthorName
                  ? ` · commits as ${project.commitAuthorName}`
                  : value.gitName
                    ? " · commits as your profile identity"
                    : ""}
              </summary>
              {harnesses.length > 0 ? <ProjectOverrides projectId={project.id} harnesses={harnesses} /> : <p>No harnesses in your profile yet.</p>}
            </details>
          ))}
        </div>
      )}
    </section>
  );
}
