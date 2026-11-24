import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { CoordinationPolicy, Project } from "../api/generated/types.gen";
import { packMayRunNative, packMayRunStructured } from "../api/harnessPacks";
import { queries } from "../api/queries";
import { harnessName, sessionPath, TERMINAL_SESSION } from "../shell/capsuleActivity";
import { pinnedLeads, type PinnedLead } from "../shell/pins";
import { useWorkingSet } from "../shell/useWorkingSet";
import { includeNotesDefault, includeNotesRequest } from "./notesText";
import {
  connectableHarnesses,
  connectionSupports,
  preferredConnectionId,
} from "./ProviderConnections";
import { Button } from "./ui/Button";
import { Dialog } from "./ui/Dialog";
import { HarnessMark } from "./ui/HarnessMark";
import { CheckIcon, ChevronIcon, TerminalIcon, ThreadIcon } from "./ui/Icons";
import type { HarnessImage } from "../api/generated/types.gen";

// A lead started here may start 16 subagents in any 24 hours. The limits
// that matter in use are the daemon's own: how many subagent Capsules run at
// once and how long the lead keeps going without the operator, both left at
// their defaults, so the operator never has to guess a number up front.
export const LEAD_POLICY = { maxChildren: 16, maxDescendants: 16 } as const;

// Sending to a pinned Thread is an ordinary operator message to it, so a
// lead's grant counts it as the operator being back (ADR 0059 section 5). A
// paused Thread is resumed first, as its Resume session button does.
async function sendToPinned(target: PinnedLead, message: string) {
  let thread = await api.thread(target.thread.id);
  if (thread.state === "paused") {
    thread = (await api.threadSession("resume", thread.id, thread.resourceVersion)).thread;
  }
  return (await api.sendThreadMessage(thread.id, thread.resourceVersion, message)).thread;
}

function pinnedLabel({ capsule, thread }: PinnedLead) {
  const lead = Boolean(thread.coordinationGrantId) && !thread.parentThreadId;
  return lead ? `${capsule.name} (lead)` : capsule.name;
}

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

function HarnessPicker({
  packs,
  value,
  disabled,
  onChange,
}: {
  packs: HarnessImage[];
  value: string;
  disabled?: boolean;
  onChange: (name: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState(0);
  const rootRef = useRef<HTMLDivElement>(null);
  const typed = useRef("");
  const typedTimer = useRef(0);
  const listId = "new-session-harness-list";
  const selected = Math.max(0, packs.findIndex((item) => item.name === value));
  const active = open ? highlight : selected;
  const current = packs[selected];

  useEffect(() => {
    if (!open) return;
    const close = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    window.addEventListener("mousedown", close);
    return () => window.removeEventListener("mousedown", close);
  }, [open]);

  function choose(index: number) {
    const pack = packs[index];
    if (!pack) return;
    onChange(pack.name);
    setHighlight(index);
    setOpen(false);
  }

  function typeAhead(key: string) {
    if (key.length !== 1 || key === " ") return false;
    typed.current = (typed.current + key).toLowerCase();
    window.clearTimeout(typedTimer.current);
    typedTimer.current = window.setTimeout(() => {
      typed.current = "";
    }, 500);
    const match = packs.findIndex((item) =>
      harnessName(item.name).toLowerCase().startsWith(typed.current),
    );
    if (match < 0) return false;
    setHighlight(match);
    if (!open) onChange(packs[match].name);
    return true;
  }

  function onKeyDown(event: { key: string; preventDefault: () => void; stopPropagation: () => void }) {
    if (disabled || packs.length === 0) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      event.stopPropagation();
      const direction = event.key === "ArrowDown" ? 1 : -1;
      if (!open) {
        setHighlight(selected);
        setOpen(true);
        return;
      }
      setHighlight((currentIndex) => (currentIndex + direction + packs.length) % packs.length);
      return;
    }
    if (event.key === "Enter") {
      // A closed picker leaves Enter for the form, which starts the Capsule.
      if (!open) return;
      event.preventDefault();
      event.stopPropagation();
      choose(highlight);
      return;
    }
    if (event.key === " ") {
      event.preventDefault();
      event.stopPropagation();
      if (!open) setOpen(true);
      else choose(highlight);
      return;
    }
    if (event.key === "Escape") {
      if (!open) return;
      event.preventDefault();
      event.stopPropagation();
      setHighlight(selected);
      setOpen(false);
      return;
    }
    if (event.key === "Tab") {
      setOpen(false);
      return;
    }
    if (typeAhead(event.key)) {
      event.preventDefault();
      event.stopPropagation();
    }
  }

  if (!current) return null;

  return (
    <div className={`harness-select${open ? " open" : ""}`} ref={rootRef} onKeyDown={onKeyDown}>
      <button
        type="button"
        className="harness-select-value"
        role="combobox"
        aria-label="Harness"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open ? `harness-option-${packs[active]?.name}` : undefined}
        disabled={disabled}
        onClick={() => {
          setHighlight(selected);
          setOpen((value) => !value);
        }}
      >
        <HarnessMark harness={current.name} size="md" />
        <span>{harnessName(current.name)}</span>
        <ChevronIcon className="chevron" />
      </button>
      {open && (
        <ul className="harness-select-list" id={listId} role="listbox" aria-label="Harness">
          {packs.map((item, index) => {
            const chosen = item.name === value;
            return (
              <li key={item.name}>
                <button
                  type="button"
                  id={`harness-option-${item.name}`}
                  role="option"
                  aria-selected={chosen}
                  className={index === highlight ? "active" : undefined}
                  onMouseEnter={() => setHighlight(index)}
                  onClick={() => choose(index)}
                >
                  <HarnessMark harness={item.name} size="md" />
                  <span>{harnessName(item.name)}</span>
                  {chosen ? (
                    <CheckIcon className="harness-select-check" />
                  ) : (
                    <span className="harness-select-check" aria-hidden="true" />
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

// NewSessionDialog starts a fresh Capsule in one Project. The Project is
// chosen only when nothing is open yet. A Thread starts without a first
// message, so the harness reports its models before the first turn. When the
// Project has pinned Threads, the dialog can send a message to one of them
// instead; a new Thread stays the default.
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
  const { projectItems, projects, items } = useWorkingSet();
  const capabilities = useQuery(queries.capabilities());
  const [harness, setHarness] = useState("");
  const [mode, setMode] = useState<"thread" | "cli">("thread");
  const [connection, setConnection] = useState<string>();
  // New Threads lead by default: the agent decides when to delegate.
  const [lead, setLead] = useState(true);
  const [approveSpawns, setApproveSpawns] = useState(false);
  // undefined follows the Project's notesDelivery (ADR 0060 section 4).
  const [includeNotes, setIncludeNotes] = useState<boolean>();
  const [error, setError] = useState("");
  // "" is a new Thread, the default; a pinned Thread is an explicit choice.
  const [targetId, setTargetId] = useState("");
  const [message, setMessage] = useState("");

  const chosenProject = projectItems.find((project) => project.id === projectId);
  const pinned = useMemo(
    () => (chosenProject ? pinnedLeads(items, chosenProject.id) : []),
    [chosenProject, items],
  );
  const target = pinned.find((item) => item.thread.id === targetId);
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
  const projectNotes = useQuery({
    ...queries.projectNotes(chosenProject?.id ?? ""),
    refetchInterval: false,
  });
  const notesDelivery = projectNotes.data?.notesDelivery;
  const notesDefault = includeNotesDefault(notesDelivery, asLead);
  const includeNotesChecked = includeNotes ?? notesDefault ?? false;

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
        // includeProjectNotes goes only when it differs from the default.
        const includeProjectNotes = includeNotesRequest(notesDelivery, asLead, includeNotesChecked);
        const intent = includeProjectNotes === undefined
          ? await api.createProjectThread(chosenProject.id, harnessId, "", undefined, coordination)
          : await api.createProjectThread(
              chosenProject.id,
              harnessId,
              "",
              undefined,
              coordination,
              undefined,
              includeProjectNotes,
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

  const send = useMutation({
    mutationFn: ({ to, text }: { to: PinnedLead; text: string }) => sendToPinned(to, text),
    onSuccess: async (thread) => {
      setError("");
      await queryClient.invalidateQueries({ queryKey: ["threads", thread.capsuleId] });
      navigate(sessionPath(thread.capsuleId, thread.id));
      onClose();
    },
    onError: (cause) => setError(normalizeAPIError(cause).message),
  });
  const busy = start.isPending || send.isPending;

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [onClose, busy]);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (target) {
      if (message.trim()) send.mutate({ to: target, text: message });
      return;
    }
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
      busy={busy}
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
          if (target instanceof HTMLButtonElement && target.type === "button" && target.getAttribute("aria-expanded") !== "false") return;
          event.preventDefault();
          formRef.current?.requestSubmit();
        }}
      >
        {pinned.length > 0 && (
          <label htmlFor="new-session-target">
            Send to
            <select
              id="new-session-target"
              value={targetId}
              disabled={busy}
              onChange={(event) => {
                setTargetId(event.target.value);
                setError("");
              }}
            >
              <option value="">New Thread</option>
              {pinned.map((item) => (
                <option key={item.thread.id} value={item.thread.id}>
                  {pinnedLabel(item)}
                </option>
              ))}
            </select>
          </label>
        )}
        {target ? (
          <>
            <label htmlFor="new-session-message">
              Message
              <textarea
                id="new-session-message"
                autoFocus
                required
                rows={5}
                maxLength={131072}
                value={message}
                disabled={busy}
                placeholder={`Tell ${target.capsule.name} what to do next…`}
                onChange={(event) => setMessage(event.target.value)}
              />
            </label>
            <p className="muted-copy">
              This goes to {target.capsule.name} as your message in its Thread
              {target.thread.state === "paused" ? ", after its session resumes" : ""}.
            </p>
          </>
        ) : (
        <>
        <HarnessPicker
          packs={packs}
          value={harnessId}
          disabled={start.isPending}
          onChange={(name) => {
            setHarness(name);
            setConnection(undefined);
          }}
        />
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
                  It hands independent or long-running work to subagent Threads on its own,
                  each in its own Capsule, and collects their results. Subagents ask you, not the lead, for
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
        {activeMode === "thread" && harnessId && notesDefault !== undefined && (
          <fieldset className="lead-options notes-option">
            <label className="checkbox-label" htmlFor="new-session-notes">
              <input
                id="new-session-notes"
                type="checkbox"
                checked={includeNotesChecked}
                disabled={start.isPending}
                onChange={(event) => setIncludeNotes(event.currentTarget.checked)}
              />
              Include Project notes
            </label>
            <p className="muted-copy">
              {notesDelivery === "leads_only"
                ? "This Project gives its notes to lead Threads and their subagents."
                : "This Project gives its notes to every Thread."}{" "}
              They arrive as files the agent reads when it needs them.
            </p>
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
        </>
        )}
        {error && (
          <p className="inline-error" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-actions">
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          {target ? (
            <Button type="submit" variant="primary" disabled={busy || !message.trim()}>
              {send.isPending ? "Sending…" : `Send to ${target.capsule.name}`}
            </Button>
          ) : (
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
          )}
        </div>
      </form>
      )}
    </Dialog>
  );
}
