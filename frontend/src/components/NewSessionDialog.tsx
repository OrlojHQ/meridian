import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { CoordinationPolicy, Project } from "../api/generated/types.gen";
import { packMayRunNative, packMayRunStructured } from "../api/harnessPacks";
import { queries } from "../api/queries";
import { harnessName, sessionPath, TERMINAL_SESSION } from "../shell/capsuleActivity";
import { useWorkingSet } from "../shell/useWorkingSet";
import {
  connectableHarnesses,
  connectionSupports,
  preferredConnectionId,
} from "./ProviderConnections";
import { Button } from "./ui/Button";
import { Dialog } from "./ui/Dialog";
import { HarnessMark } from "./ui/HarnessMark";
import { TerminalIcon, ThreadIcon } from "./ui/Icons";

// A lead started here gets the largest lifetime subagent total the daemon
// allows. The limits that matter in use are the daemon's own: how many
// subagent Capsules run at once and the grant deadline, both left at their
// defaults, so the operator never has to guess a number up front.
const LEAD_POLICY = { maxChildren: 16, maxDescendants: 16 } as const;

function recentProjects(projects: Project[]) {
  return [...projects].sort(
    (left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt) || left.name.localeCompare(right.name),
  );
}

function ProjectPicker({
  projects,
  onSelect,
}: {
  projects: Project[];
  onSelect: (id: string) => void;
}) {
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const recent = useMemo(() => recentProjects(projects), [projects]);
  const matches = recent.filter((project) =>
    project.name.toLowerCase().includes(query.trim().toLowerCase()),
  );
  const selected = Math.min(index, Math.max(matches.length - 1, 0));

  function choose(id: string) {
    onSelect(id);
  }

  function onKeyDown(event: { key: string; preventDefault: () => void }) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const direction = event.key === "ArrowDown" ? 1 : -1;
      setIndex((current) => {
        const next = Math.min(current, Math.max(matches.length - 1, 0)) + direction;
        return (next + matches.length) % matches.length;
      });
      return;
    }
    if (event.key === "Enter" && matches[selected]) {
      event.preventDefault();
      choose(matches[selected].id);
    }
  }

  return (
    <div className="project-picker" onKeyDown={onKeyDown}>
      <label>
        Find a project
        <input
          autoFocus
          role="searchbox"
          value={query}
          placeholder="Search projects"
          onChange={(event) => {
            setQuery(event.target.value);
            setIndex(0);
          }}
        />
      </label>
      {matches.length === 0 ? (
        <p className="field-hint">No matching project.</p>
      ) : (
        <ul className="project-picker-list" role="listbox" aria-label="Projects">
          {matches.map((project, itemIndex) => (
            <li key={project.id}>
              <button
                type="button"
                role="option"
                aria-selected={itemIndex === selected}
                onMouseEnter={() => setIndex(itemIndex)}
                onClick={() => choose(project.id)}
              >
                {project.name}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// NewSessionDialog starts a fresh Capsule in one Project. The Project is
// chosen only when nothing is open yet. A Thread starts without a first
// message, so the harness reports its models before the first turn.
export function NewSessionDialog({
  projectId,
  onProjectChange,
  onClose,
}: {
  projectId: string;
  onProjectChange: (id: string) => void;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const closeRef = useRef<HTMLButtonElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const { projectItems, projects } = useWorkingSet();
  const capabilities = useQuery(queries.capabilities());
  const [harness, setHarness] = useState("");
  const [mode, setMode] = useState<"thread" | "cli">("thread");
  const [connection, setConnection] = useState<string>();
  const [lead, setLead] = useState(false);
  const [approveSpawns, setApproveSpawns] = useState(false);
  const [error, setError] = useState("");

  const chosenProject = projectItems.find((project) => project.id === projectId);
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
  const coordinationSupported = capabilities.data?.coordination?.supported === true;
  const asLead = activeMode === "thread" && coordinationSupported && lead;

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
  const blocked = activeMode === "thread" && connectable &&
    !usable.some((item) => item.id === selectedConnection);

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
        const coordination: CoordinationPolicy | undefined = asLead
          ? {
              ...LEAD_POLICY,
              ...(approveSpawns ? { spawnApproval: "operator" as const } : {}),
            }
          : undefined;
        const intent = await api.createProjectThread(
          chosenProject.id,
          harnessId,
          "",
          undefined,
          coordination,
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
      onClose();
    },
    onError: (cause) => setError(normalizeAPIError(cause).message),
  });

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !start.isPending) onClose();
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [onClose, start.isPending]);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (blocked || !chosenProject) return;
    start.mutate();
  }

  const picking = !projects.isPending && !chosenProject;

  return (
    <Dialog
      labelledBy="new-session-title"
      eyebrow={chosenProject ? chosenProject.name : undefined}
      title={picking ? "Choose a project" : "New Capsule"}
      closeLabel="Close new Capsule"
      closeRef={closeRef}
      onClose={onClose}
      busy={start.isPending}
      className="new-session-dialog"
    >
      {projects.isPending ? (
        <p role="status">Loading projects…</p>
      ) : picking ? (
        <ProjectPicker projects={projectItems} onSelect={onProjectChange} />
      ) : (
      <form
        ref={formRef}
        className="launcher-form"
        onSubmit={submit}
        onKeyDown={(event) => {
          if (event.key !== "Enter" || event.shiftKey || event.altKey || event.metaKey || event.ctrlKey) return;
          const target = event.target;
          if (target instanceof HTMLTextAreaElement) return;
          if (target instanceof HTMLButtonElement && target.type === "button") return;
          event.preventDefault();
          formRef.current?.requestSubmit();
        }}
      >
        <div className="harness-tiles" role="radiogroup" aria-label="Harness">
          {packs.map((item) => (
            <label key={item.name} className={item.name === harnessId ? "is-selected" : undefined}>
              <input
                type="radio"
                name="new-session-harness"
                value={item.name}
                checked={item.name === harnessId}
                disabled={start.isPending}
                onChange={() => {
                  setHarness(item.name);
                  setConnection(undefined);
                }}
              />
              <HarnessMark harness={item.name} size="md" />
              <span>{harnessName(item.name)}</span>
            </label>
          ))}
        </div>
        <div className="session-mode" role="tablist" aria-label="How to start">
          <span title={structured ? undefined : "This harness has no structured session"}>
            <button
              type="button"
              role="tab"
              aria-selected={activeMode === "thread"}
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
              disabled={!native}
              onClick={() => setMode("cli")}
            >
              <TerminalIcon />
              CLI
            </button>
          </span>
        </div>
        {packs.length === 0 && (
          <p className="field-hint">
            <Link to={`/ui/projects/${chosenProject?.id ?? ""}`} onClick={onClose}>
              Add a harness pack
            </Link>{" "}
            to {chosenProject?.name ?? "this Project"} first.
          </p>
        )}
        {activeMode === "thread" && coordinationSupported && harnessId && (
          <fieldset className="lead-options">
            <label className="checkbox-label" htmlFor="new-session-lead">
              <input
                id="new-session-lead"
                type="checkbox"
                checked={lead}
                disabled={start.isPending}
                onChange={(event) => setLead(event.currentTarget.checked)}
              />
              Lead agent with subagents
            </label>
            {lead && (
              <>
                <p className="muted-copy">
                  Ask it to split the work and it starts subagent Threads, each in its own
                  Capsule, and collects their results. Subagents ask you, not the lead, for
                  permissions.
                </p>
                <label className="checkbox-label" htmlFor="new-session-approval">
                  <input
                    id="new-session-approval"
                    type="checkbox"
                    checked={approveSpawns}
                    disabled={start.isPending}
                    onChange={(event) => setApproveSpawns(event.currentTarget.checked)}
                  />
                  Approve each subagent spawn
                </label>
              </>
            )}
          </fieldset>
        )}
        {blocked && (
          <p className="field-hint" id="new-session-provider-required">
            Thread mode needs a provider connection.{" "}
            <Link to="/ui/settings/harnesses" onClick={onClose}>
              Connect a provider
            </Link>
            {native && <> or switch to CLI to sign in inside {harnessName(harnessId)}</>}.
          </p>
        )}
        {error && (
          <p className="inline-error" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-actions">
          <Button variant="secondary" disabled={start.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            variant="primary"
            aria-describedby={blocked ? "new-session-provider-required" : undefined}
            disabled={
              start.isPending ||
              !harnessId ||
              blocked ||
              connections.isPending ||
              grant.isPending
            }
          >
            {start.isPending ? "Starting…" : "Start"}
          </Button>
        </div>
      </form>
      )}
    </Dialog>
  );
}
