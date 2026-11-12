import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
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
import { Select } from "./ui/Field";
import { TerminalIcon, ThreadIcon } from "./ui/Icons";

// NewSessionDialog starts a fresh Capsule. It asks only what to start: a
// Thread or the harness CLI, in which Project, with which harness. A Thread
// starts without a first message, so the harness reports its models and the
// operator chooses one in the Thread before the first turn.
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
  const { projectItems } = useWorkingSet();
  const capabilities = useQuery(queries.capabilities());
  const [harness, setHarness] = useState("");
  const [mode, setMode] = useState<"thread" | "cli">("thread");
  const [connection, setConnection] = useState<string>();
  const [error, setError] = useState("");

  const chosenProject = projectItems.find((project) => project.id === projectId) ?? projectItems[0];
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
        const intent = await api.createProjectThread(chosenProject.id, harnessId);
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
    if (blocked) return;
    start.mutate();
  }

  return (
    <Dialog
      labelledBy="new-session-title"
      title="New Capsule"
      closeLabel="Close new Capsule"
      closeRef={closeRef}
      onClose={onClose}
      busy={start.isPending}
      className="new-session-dialog"
    >
      <form className="launcher-form" onSubmit={submit}>
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
        <label htmlFor="new-session-project">
          Project
          <Select
            id="new-session-project"
            value={chosenProject?.id ?? ""}
            disabled={start.isPending}
            onChange={(event) => {
              onProjectChange(event.target.value);
              setHarness("");
              setConnection(undefined);
            }}
          >
            {projectItems.map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </Select>
        </label>
        <label htmlFor="new-session-harness">
          Harness
          <Select
            id="new-session-harness"
            value={harnessId}
            disabled={start.isPending || packs.length === 0}
            onChange={(event) => {
              setHarness(event.target.value);
              setConnection(undefined);
            }}
          >
            {packs.map((item) => (
              <option key={item.name} value={item.name}>
                {harnessName(item.name)}
              </option>
            ))}
          </Select>
        </label>
        {packs.length === 0 && (
          <p className="field-hint">
            <Link to={`/ui/projects/${chosenProject?.id ?? ""}`} onClick={onClose}>
              Add a harness pack
            </Link>{" "}
            to {chosenProject?.name ?? "this Project"} first.
          </p>
        )}
        {connectable && available.length > 1 && (
          <label htmlFor="new-session-connection">
            Provider connection
            <Select
              id="new-session-connection"
              value={selectedConnection}
              disabled={start.isPending}
              onChange={(event) => setConnection(event.target.value)}
            >
              <option value="">Sign in inside the harness</option>
              {available.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}
                </option>
              ))}
            </Select>
          </label>
        )}
        {harnessId && !blocked && (
          <p className="field-hint">
            {activeMode === "thread"
              ? `Pick ${harnessName(harnessId)}'s model and effort in the Thread before you send anything.`
              : connectable && !selectedConnection
                ? `You can sign in inside ${harnessName(harnessId)} after it opens.`
                : `Opens ${harnessName(harnessId)} in a terminal.`}
          </p>
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
    </Dialog>
  );
}
