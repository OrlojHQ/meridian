import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useOutletContext } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import { packMayRunNative, packMayRunStructured } from "../api/harnessPacks";
import { queries } from "../api/queries";
import {
  connectableHarnesses,
  connectionSupports,
  preferredConnectionId,
} from "./ProviderConnections";
import { harnessName, relativeTime, sessionPath, TERMINAL_SESSION } from "../shell/capsuleActivity";
import type { ShellOutletContext } from "../shell/AppShell";
import { useWorkingSet } from "../shell/useWorkingSet";
import { TerminalIcon, ThreadIcon } from "./ui/Icons";

export function HomeComposer() {
  const outlet = useOutletContext<ShellOutletContext | undefined>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const taskRef = useRef<HTMLTextAreaElement>(null);
  const { projectItems, items } = useWorkingSet();
  const capabilities = useQuery(queries.capabilities());
  const [projectId, setProjectId] = useState("");
  const [harness, setHarness] = useState("");
  const [mode, setMode] = useState<"thread" | "cli">("thread");
  const [prompt, setPrompt] = useState("");
  const [connection, setConnection] = useState<string>();
  const [error, setError] = useState("");

  const chosenProject =
    projectItems.find((project) => project.id === (outlet?.projectId || projectId)) ??
    projectItems[0];
  const packs = chosenProject?.harnessImages ?? [];
  const catalog = capabilities.data?.harnessImages ?? [];
  const pack = packs.find((item) => item.name === harness) ?? packs[0];
  const harnessId = pack?.name ?? "";
  const structured =
    capabilities.data?.structured === true &&
    pack !== undefined &&
    packMayRunStructured(pack, catalog);
  const native = pack === undefined || packMayRunNative(pack, catalog);
  const activeMode = structured && (!native || mode === "thread") ? "thread" : "cli";

  useEffect(() => {
    taskRef.current?.focus();
  }, []);

  const connections = useQuery({
    queryKey: ["provider-connections"],
    queryFn: api.providerConnections,
  });
  const grant = useQuery({
    queryKey: ["project-connection", chosenProject?.id, harnessId],
    queryFn: () => api.projectConnection(chosenProject!.id, harnessId),
    enabled: Boolean(chosenProject && harnessId),
  });
  const connectable =
    connectableHarnesses.includes(harnessId) && connections.data?.enabled === true;
  const available =
    connections.data?.items.filter(
      (item) => !item.revoked && connectionSupports(harnessId, item),
    ) ?? [];
  const selectedConnection = preferredConnectionId(
    connection,
    grant.isSuccess || grant.isError,
    grant.data?.connectionId,
    available,
  );
  const usable = available.filter((item) => !item.needsSignIn);
  const blocked = connectable && !usable.some((item) => item.id === selectedConnection);

  const recent = useMemo(
    () =>
      items
        .filter((item) => item.activity.group !== "attention")
        .sort(
          (left, right) =>
            Date.parse(right.activity.since ?? right.capsule.updatedAt) -
            Date.parse(left.activity.since ?? left.capsule.updatedAt),
        )
        .slice(0, 5),
    [items],
  );

  const start = useMutation({
    mutationFn: async () => {
      if (!chosenProject || !harnessId) throw new Error("Choose a project and a harness");
      if (
        connectable &&
        selectedConnection &&
        selectedConnection !== (grant.data?.connectionId ?? "")
      ) {
        await api.grantConnection(chosenProject.id, harnessId, selectedConnection);
      }
      if (activeMode === "thread") {
        const intent = await api.createProjectThread(
          chosenProject.id,
          harnessId,
          prompt.trim(),
        );
        return sessionPath(intent.capsuleId, intent.threadId);
      }
      const capsule = await api.createCapsule(
        chosenProject.id,
        `${harnessId}-${crypto.randomUUID().slice(0, 8)}`,
        harnessId,
      );
      return sessionPath(capsule.id, TERMINAL_SESSION);
    },
    onSuccess: async (path) => {
      setError("");
      await queryClient.invalidateQueries({ queryKey: ["capsules"] });
      navigate(path);
    },
    onError: (cause) => setError(normalizeAPIError(cause).message),
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    if (activeMode === "thread" && prompt.trim() === "") return;
    start.mutate();
  }

  if (projectItems.length === 0) {
    return (
      <section className="home-composer empty-state">
        <h1>No projects yet</h1>
        <p>A Project points Meridian at a repository. Every session runs in its own Capsule.</p>
        {outlet?.openNewProject && (
          <button type="button" className="primary-button" onClick={outlet.openNewProject}>
            New project
          </button>
        )}
      </section>
    );
  }

  return (
    <section className="home-composer">
      <form className="launch-card" onSubmit={submit}>
        {activeMode === "thread" && (
          <textarea
            ref={taskRef}
            className="launch-task"
            value={prompt}
            placeholder="What should we work on?"
            aria-label="What should we work on?"
            rows={4}
            required
            onChange={(event) => setPrompt(event.target.value)}
          />
        )}
        <div className="launch-bar">
          <div className="segmented-control" role="tablist" aria-label="How to start">
            <span title={structured ? undefined : "This harness has no structured session"}>
              <button
                type="button"
                role="tab"
                aria-selected={activeMode === "thread"}
                className={activeMode === "thread" ? "active" : undefined}
                disabled={!structured}
                onClick={() => setMode("thread")}
              >
                <ThreadIcon />
                Thread
              </button>
            </span>
            <span title={native ? undefined : "This harness has no terminal"}>
              <button
                type="button"
                role="tab"
                aria-selected={activeMode === "cli"}
                className={activeMode === "cli" ? "active" : undefined}
                disabled={!native}
                onClick={() => setMode("cli")}
              >
                <TerminalIcon />
                CLI
              </button>
            </span>
          </div>
          <label className="picker">
            <span className="sr-only">Project</span>
            <select
              aria-label="Project"
              value={chosenProject?.id ?? ""}
              onChange={(event) => {
                setProjectId(event.target.value);
                outlet?.setProjectId(event.target.value);
                setHarness("");
              }}
            >
              {projectItems.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name}
                </option>
              ))}
            </select>
          </label>
          <label className="picker">
            <span className="sr-only">Harness</span>
            <select
              aria-label="Harness"
              value={harnessId}
              onChange={(event) => setHarness(event.target.value)}
            >
              {packs.map((item) => (
                <option key={item.name} value={item.name}>
                  {harnessName(item.name)}
                </option>
              ))}
            </select>
          </label>
          {connectable && available.length > 1 && (
            <label className="picker">
              <span className="sr-only">Provider connection</span>
              <select
                aria-label="Provider connection"
                value={selectedConnection}
                onChange={(event) => setConnection(event.target.value)}
              >
                <option value="">Sign in inside the harness</option>
                {available.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <button
            type="submit"
            className="primary-button"
            disabled={
              start.isPending ||
              !harnessId ||
              blocked ||
              connections.isPending ||
              grant.isPending
            }
          >
            {start.isPending ? "Starting…" : "Start"}
          </button>
        </div>
        {packs.length === 0 && (
          <p className="launch-hint">
            <Link to={`/ui/projects/${chosenProject?.id ?? ""}`}>Add a harness pack</Link>
          </p>
        )}
        {blocked && (
          <p className="launch-hint">
            <Link to="/ui/settings/harnesses">Connect a provider</Link>
          </p>
        )}
        {error && (
          <p className="error-state" role="alert">
            {error}
          </p>
        )}
      </form>
      {recent.length > 0 && (
        <section className="home-recent" aria-label="Recent Capsules">
          <h2>Recent</h2>
          <ul>
            {recent.map((item) => (
              <li key={item.capsule.id}>
                <Link to={sessionPath(item.capsule.id)}>
                  {item.sessions.some((session) => session.kind === "thread" && !session.archived) ? (
                    <ThreadIcon />
                  ) : (
                    <TerminalIcon />
                  )}
                  <strong>{item.capsule.name}</strong>
                  <em>{item.activity.label}</em>
                  <time dateTime={item.activity.since}>{relativeTime(item.activity.since)}</time>
                </Link>
              </li>
            ))}
          </ul>
        </section>
      )}
    </section>
  );
}
