import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { CheckCommand, ProjectChecks as Checks } from "../api/generated/types.gen";

type Row = { name: string; command: string; timeout: string };

function commandLine(argv: string[]) {
  return argv.join(" ");
}

function rowsFrom(checks: CheckCommand[]): Row[] {
  if (checks.length === 0) return [{ name: "test", command: "", timeout: "600" }];
  return checks.map((check) => ({
    name: check.name,
    command: commandLine(check.argv),
    timeout: String(check.timeoutSeconds || 600),
  }));
}

function Editor({ projectId, checks, embedded }: { projectId: string; checks: Checks; embedded: boolean }) {
  const [rows, setRows] = useState<Row[]>(() => rowsFrom(checks.checks));
  const [autoRework, setAutoRework] = useState(checks.autoRework);
  const [attempts, setAttempts] = useState(String(checks.maxReworkAttempts));
  const client = useQueryClient();
  const suggestion = useQuery({
    queryKey: ["check-suggestion", projectId],
    queryFn: ({ signal }) => api.suggestProjectChecks(projectId, undefined, signal),
  });
  const mutation = useMutation({
    mutationFn: () => {
      const parsed = rows
        .map((row) => ({
          name: row.name.trim(),
          argv: row.command.trim().split(/\s+/).filter(Boolean),
          timeoutSeconds: Number(row.timeout) || 600,
        }))
        .filter((row) => row.name !== "" && row.argv.length > 0);
      const maxReworkAttempts = Number(attempts);
      return api.updateProjectChecks(projectId, {
        checks: parsed,
        autoRework,
        maxReworkAttempts: Number.isFinite(maxReworkAttempts) ? maxReworkAttempts : 3,
        expectedResourceVersion: checks.resourceVersion,
      });
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["project-checks", projectId] });
    },
  });
  function submit(event: FormEvent) {
    event.preventDefault();
    mutation.mutate();
  }
  function update(index: number, patch: Partial<Row>) {
    setRows((current) => current.map((row, rowIndex) => (rowIndex === index ? { ...row, ...patch } : row)));
  }
  const suggested = suggestion.data?.checks ?? [];
  const showSuggestion = suggested.length > 0 && checks.checks.length === 0;
  return (
    <form className="project-checks" onSubmit={submit}>
      {!embedded && (
        <p>
          When the agent finishes, Meridian runs these commands inside the Capsule.
          If one fails, Meridian sends the failure back and tries again, up to the limit.
        </p>
      )}
      {showSuggestion && (
        <p className="check-suggestion">
          This repository suggests {suggested.map((item) => item.name).join(", ")}.{" "}
          <button
            type="button"
            onClick={() => setRows(rowsFrom(suggested))}
          >
            Use them
          </button>
        </p>
      )}
      <ol className="check-rows">
        {rows.map((row, index) => (
          <li key={index}>
            <label>
              Name
              <input
                aria-label={`Check ${index + 1} name`}
                value={row.name}
                maxLength={64}
                onChange={(event) => update(index, { name: event.target.value })}
              />
            </label>
            <label>
              Command
              <input
                aria-label={`Check ${index + 1} command`}
                value={row.command}
                placeholder="make test"
                onChange={(event) => update(index, { command: event.target.value })}
              />
            </label>
            <label>
              Timeout (seconds)
              <input
                aria-label={`Check ${index + 1} timeout`}
                inputMode="numeric"
                value={row.timeout}
                onChange={(event) => update(index, { timeout: event.target.value })}
              />
            </label>
            <button
              type="button"
              onClick={() => setRows((current) => current.filter((_, rowIndex) => rowIndex !== index))}
              disabled={rows.length === 1 && row.command === ""}
            >
              Remove
            </button>
          </li>
        ))}
      </ol>
      <button type="button" onClick={() => setRows((current) => [...current, { name: "", command: "", timeout: "600" }])}>
        Add a check
      </button>
      <label className="check-label">
        <input type="checkbox" checked={autoRework} onChange={(event) => setAutoRework(event.target.checked)} />
        Automatically send failures back to the agent
      </label>
      <label>
        Attempts
        <input
          aria-label="Attempts"
          inputMode="numeric"
          value={attempts}
          onChange={(event) => setAttempts(event.target.value)}
        />
      </label>
      <p className="launch-hint">
        3 runs the checks up to three times. 0 runs them once and stops, even if they fail.
      </p>
      <button type="submit" disabled={mutation.isPending}>Save checks</button>
      {mutation.isError && <p role="alert">{normalizeAPIError(mutation.error).message}</p>}
    </form>
  );
}

export function ProjectChecks({ projectId, embedded = false }: { projectId: string; embedded?: boolean }) {
  const query = useQuery({
    queryKey: ["project-checks", projectId],
    queryFn: ({ signal }) => api.projectChecks(projectId, signal),
  });
  return (
    <section
      className={embedded ? "settings-embedded-checks" : "project-checks-section"}
      aria-label="Checks"
    >
      {!embedded && <h2>Checks</h2>}
      {query.isPending && <p role="status">Loading checks…</p>}
      {query.isError && <p role="alert">{normalizeAPIError(query.error).message}</p>}
      {query.data && <Editor key={query.data.resourceVersion} projectId={projectId} checks={query.data} embedded={embedded} />}
    </section>
  );
}
