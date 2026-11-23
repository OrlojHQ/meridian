import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { HarnessImage, Project, Routine, RoutineFire } from "../api/generated/types.gen";
import { packMayRunStructured } from "../api/harnessPacks";
import { queries } from "../api/queries";
import { harnessName, sessionPath } from "../shell/capsuleActivity";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { Input, Select, TextArea } from "./ui/Field";
import { HarnessMark } from "./ui/HarnessMark";
import { PlusIcon } from "./ui/Icons";
import { TimeZonePicker } from "./TimeZonePicker";
import {
  browserTimeZone,
  buildCadence,
  clockTime,
  describeCadence,
  disabledText,
  durationText,
  fireKeptCapsule,
  fireSummary,
  formatWhen,
  nextRunHint,
  parseCadence,
  triggerLabel,
  weekdayNames,
  type CadenceMode,
} from "./routineText";

// Routines (ADR 0057). A routine starts a fresh Capsule
// with a fixed prompt on a clock. Runs with no changes delete their Capsule;
// the rest wait for the operator. A routine never ships.

const allRoutinesKey = ["routines"];

type FormState = {
  name: string;
  harness: string;
  model: string;
  effort: string;
  prompt: string;
  mode: CadenceMode;
  time: string;
  minute: string;
  weekday: string;
  custom: string;
  timeZone: string;
  maxRunMinutes: string;
  catchUpMinutes: string;
  maxKept: string;
  // Scheduled Threads get Project notes only when the routine opts in
  // (ADR 0060 section 4).
  includeNotes: boolean;
};

function formFrom(routine: Routine | undefined, harness: string): FormState {
  if (!routine) {
    return {
      name: "", harness, model: "", effort: "", prompt: "",
      mode: "weekly", time: "06:00", minute: "0", weekday: "1", custom: "0 6 * * 1",
      timeZone: browserTimeZone(), maxRunMinutes: "120", catchUpMinutes: "60", maxKept: "5",
      includeNotes: false,
    };
  }
  const parts = parseCadence(routine.cadence);
  return {
    name: routine.name, harness: routine.harness, model: routine.model ?? "",
    effort: routine.effort ?? "", prompt: routine.prompt, mode: parts.mode,
    time: clockTime(parts.hour, parts.minute), minute: String(parts.minute),
    weekday: String(parts.weekday), custom: routine.cadence, timeZone: routine.timeZone,
    maxRunMinutes: String(Math.round(routine.maxRunSeconds / 60)),
    catchUpMinutes: String(Math.round(routine.catchUpSeconds / 60)),
    maxKept: String(routine.maxKept),
    includeNotes: routine.includeNotes === true,
  };
}

function ScheduleHint({ cadence, zone }: { cadence: string; zone: string }) {
  const next = nextRunHint(cadence, zone);
  const where = zone || "UTC";
  const when = next
    ? ` Next: ${next.there}${next.here ? ` (${next.here} your time)` : ""}.`
    : "";
  return (
    <p className="launch-hint">
      <span className="mono">{cadence}</span>
      {` in ${where}.${when} Runs must be at least 15 minutes apart.`}
    </p>
  );
}

function cadenceOf(form: FormState) {
  const [hour, minute] = form.time.split(":").map(Number);
  return buildCadence({
    mode: form.mode,
    hour: Number.isFinite(hour) ? hour : 0,
    minute: form.mode === "hourly" ? Number(form.minute) || 0 : Number.isFinite(minute) ? minute : 0,
    weekday: Number(form.weekday) || 0,
    custom: form.custom,
  });
}

function ModelField({
  label, value, options, onChange, hint,
}: {
  label: string;
  value: string;
  options: { id: string; label: string }[];
  onChange: (value: string) => void;
  hint: string;
}) {
  const known = options.some((option) => option.id === value);
  if (options.length === 0) {
    return (
      <label className="routine-field">
        {label}
        <Input value={value} placeholder="Harness default" onChange={(event) => onChange(event.target.value)} />
        <span className="launch-hint">{hint}</span>
      </label>
    );
  }
  return (
    <label className="routine-field">
      {label}
      <Select value={value} onChange={(event) => onChange(event.target.value)}>
        <option value="">Harness default</option>
        {options.map((option) => (
          <option key={option.id} value={option.id}>{option.label}</option>
        ))}
        {value && !known && <option value={value}>{value} (not reported)</option>}
      </Select>
    </label>
  );
}

function RoutineForm({
  project, routine, harnesses, onDone,
}: {
  project: Project;
  routine?: Routine;
  harnesses: string[];
  onDone: () => void;
}) {
  const client = useQueryClient();
  const [form, setForm] = useState<FormState>(() => formFrom(routine, harnesses[0] ?? ""));
  const [error, setError] = useState("");
  const set = (patch: Partial<FormState>) => setForm((current) => ({ ...current, ...patch }));
  const models = useQuery({
    queryKey: ["reported-models", project.id, form.harness],
    queryFn: ({ signal }) => api.reportedModels(project.id, form.harness, signal),
    enabled: form.harness !== "",
  });
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name: form.name.trim(), harness: form.harness, model: form.model.trim(), effort: form.effort.trim(),
        prompt: form.prompt, cadence: cadenceOf(form), timeZone: form.timeZone.trim(),
        maxRunSeconds: (Number(form.maxRunMinutes) || 120) * 60,
        catchUpSeconds: (Number(form.catchUpMinutes) || 0) * 60,
        maxKept: Number(form.maxKept) || 5,
        includeNotes: form.includeNotes,
      };
      if (routine) {
        return api.updateRoutine(routine.id, { ...body, expectedResourceVersion: routine.resourceVersion });
      }
      return api.createRoutine(project.id, {
        ...body, model: body.model || undefined, effort: body.effort || undefined,
      });
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: allRoutinesKey });
      onDone();
    },
    onError: (failure) => setError(normalizeAPIError(failure).message),
  });
  function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!/^[a-z0-9-]{1,48}$/.test(form.name.trim())) {
      setError("Use 1 to 48 lowercase letters, digits, or dashes for the name.");
      return;
    }
    if (form.prompt.trim() === "") {
      setError("Write the prompt the run starts with.");
      return;
    }
    save.mutate();
  }
  const reported = models.data;
  const modelHint = "No session of this harness has reported its models yet. Type a model ID, or leave it empty.";
  const cadence = cadenceOf(form);
  return (
    <form className="routine-form" onSubmit={submit} aria-label={routine ? `Edit ${routine.name}` : "New routine"}>
      <div className="routine-form-grid">
        <label className="routine-field">
          Name
          <Input
            value={form.name}
            placeholder="deps-weekly"
            maxLength={48}
            onChange={(event) => set({ name: event.target.value.toLowerCase() })}
          />
        </label>
        <label className="routine-field">
          Harness
          <Select value={form.harness} onChange={(event) => set({ harness: event.target.value, model: "", effort: "" })}>
            {harnesses.map((name) => <option key={name} value={name}>{harnessName(name)}</option>)}
          </Select>
        </label>
        <ModelField
          label="Model"
          value={form.model}
          options={reported?.models ?? []}
          onChange={(model) => set({ model })}
          hint={modelHint}
        />
        <ModelField
          label="Effort"
          value={form.effort}
          options={reported?.efforts ?? []}
          onChange={(effort) => set({ effort })}
          hint="Leave empty for the model's default."
        />
      </div>
      <label className="routine-field">
        Prompt
        <TextArea
          rows={5}
          value={form.prompt}
          maxLength={32768}
          placeholder="Update minor and patch dependencies, run the tests, and propose a delivery if anything changed."
          onChange={(event) => set({ prompt: event.target.value })}
        />
      </label>
      <fieldset className="routine-when-fields">
        <legend>When</legend>
        <Select aria-label="Repeat" value={form.mode} onChange={(event) => set({ mode: event.target.value as CadenceMode })}>
          <option value="hourly">Every hour</option>
          <option value="daily">Every day</option>
          <option value="weekdays">Weekdays</option>
          <option value="weekly">Every week</option>
          <option value="custom">Custom cron</option>
        </Select>
        {form.mode === "weekly" && (
          <Select aria-label="Day" value={form.weekday} onChange={(event) => set({ weekday: event.target.value })}>
            {weekdayNames.map((day, index) => <option key={day} value={index}>{day}</option>)}
          </Select>
        )}
        {form.mode === "hourly" && (
          <label className="routine-inline">
            at minute
            <Input aria-label="Minute" inputMode="numeric" value={form.minute} onChange={(event) => set({ minute: event.target.value })} />
          </label>
        )}
        {(form.mode === "daily" || form.mode === "weekdays" || form.mode === "weekly") && (
          <label className="routine-inline">
            at
            <Input aria-label="Time" type="time" value={form.time} onChange={(event) => set({ time: event.target.value })} />
          </label>
        )}
        {form.mode === "custom" && (
          <Input
            aria-label="Cron expression"
            className="mono"
            value={form.custom}
            placeholder="0 6 * * 1"
            onChange={(event) => set({ custom: event.target.value })}
          />
        )}
        <TimeZonePicker value={form.timeZone} onChange={(timeZone) => set({ timeZone })} />
      </fieldset>
      <ScheduleHint cadence={cadence} zone={form.timeZone} />
      <div className="routine-notes">
        <label className="checkbox-label" htmlFor={`routine-notes-${routine?.id ?? "new"}`}>
          <input
            id={`routine-notes-${routine?.id ?? "new"}`}
            type="checkbox"
            role="switch"
            aria-describedby={`routine-notes-hint-${routine?.id ?? "new"}`}
            checked={form.includeNotes}
            onChange={(event) => set({ includeNotes: event.currentTarget.checked })}
          />
          Include Project notes
        </label>
        <p className="launch-hint" id={`routine-notes-hint-${routine?.id ?? "new"}`}>
          Each run gets the Project&apos;s notes as files. A run has no one watching, so this stays off unless you turn it on.
        </p>
      </div>
      <details className="routine-limits">
        <summary>Limits</summary>
        <div className="routine-form-grid">
          <label className="routine-field">
            Stop a run after (minutes)
            <Input inputMode="numeric" value={form.maxRunMinutes} onChange={(event) => set({ maxRunMinutes: event.target.value })} />
          </label>
          <label className="routine-field">
            Catch up a missed run within (minutes)
            <Input inputMode="numeric" value={form.catchUpMinutes} onChange={(event) => set({ catchUpMinutes: event.target.value })} />
          </label>
          <label className="routine-field">
            Pause firing while this many runs wait for you
            <Input inputMode="numeric" value={form.maxKept} onChange={(event) => set({ maxKept: event.target.value })} />
          </label>
        </div>
      </details>
      {error && <p role="alert">{error}</p>}
      <div className="routine-form-actions">
        <Button type="submit" variant="primary" disabled={save.isPending}>
          {routine ? "Save routine" : "Create routine"}
        </Button>
        <Button onClick={onDone}>Cancel</Button>
      </div>
    </form>
  );
}

function FireRow({ fire }: { fire: RoutineFire }) {
  const summary = fireSummary(fire);
  return (
    <li className="routine-fire">
      <span className="routine-fire-time">{formatWhen(fire.scheduledFor)}</span>
      <span className="routine-fire-trigger">{triggerLabel(fire.trigger)}</span>
      <Badge tone={summary.tone}>{summary.label}</Badge>
      {fireKeptCapsule(fire) && fire.capsuleId && (
        <Link className="routine-fire-link" to={sessionPath(fire.capsuleId)}>Open Capsule</Link>
      )}
    </li>
  );
}

function FireHistory({ routineId }: { routineId: string }) {
  const fires = useQuery({
    queryKey: ["routine-fires", routineId],
    queryFn: ({ signal }) => api.routineFires(routineId, signal),
    refetchInterval: 15_000,
  });
  if (fires.isPending) return <p role="status">Loading runs…</p>;
  if (fires.isError) return <p role="alert">{normalizeAPIError(fires.error).message}</p>;
  if (fires.data.items.length === 0) return <p className="launch-hint">No runs yet.</p>;
  return (
    <ol className="routine-fires" aria-label="Recent runs">
      {fires.data.items.map((fire) => <FireRow key={fire.id} fire={fire} />)}
    </ol>
  );
}

function RoutineCard({
  project, routine, harnesses,
}: {
  project: Project;
  routine: Routine;
  harnesses: string[];
}) {
  const client = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [history, setHistory] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: allRoutinesKey });
    await client.invalidateQueries({ queryKey: ["routine-fires", routine.id] });
  };
  const toggle = useMutation({
    mutationFn: () => api.updateRoutine(routine.id, {
      enabled: !routine.enabled, expectedResourceVersion: routine.resourceVersion,
    }),
    onSuccess: refresh,
  });
  const run = useMutation({ mutationFn: () => api.runRoutine(routine.id), onSuccess: refresh });
  const remove = useMutation({
    mutationFn: () => api.deleteRoutine(routine.id, routine.resourceVersion),
    onSuccess: refresh,
  });
  const failure = [toggle, run, remove].find((mutation) => mutation.isError)?.error;
  if (editing) {
    return (
      <li className="routine-card">
        <RoutineForm project={project} routine={routine} harnesses={harnesses} onDone={() => setEditing(false)} />
      </li>
    );
  }
  const current = routine.active ?? routine.lastFire;
  const summary = current ? fireSummary(current) : undefined;
  const off = disabledText(routine);
  return (
    <li className="routine-card">
      <div className="routine-card-head">
        <span className="routine-name mono">{routine.name}</span>
        <Badge tone={routine.enabled ? "ready" : "paused"}>{routine.enabled ? "On" : "Off"}</Badge>
        <span className="routine-when">
          {describeCadence(routine.cadence)} · {routine.timeZone}
        </span>
      </div>
      <div className="routine-meta">
        <span className="routine-agent">
          <HarnessMark harness={routine.harness} size="sm" />
          {harnessName(routine.harness)}
          {routine.model ? ` · ${routine.model}` : " · default model"}
          {routine.effort ? ` · ${routine.effort} effort` : ""}
          {routine.includeNotes ? " · gets Project notes" : ""}
        </span>
        <span>
          {routine.enabled && routine.nextFireAt ? `Next: ${formatWhen(routine.nextFireAt)}` : "Not scheduled"}
        </span>
      </div>
      <p className="routine-prompt">{routine.prompt}</p>
      <div className="routine-status">
        {summary && current ? (
          <>
            <span className="launch-hint">{routine.active ? "Now" : "Last run"}</span>
            <Badge tone={summary.tone}>{summary.label}</Badge>
            {fireKeptCapsule(current) && current.capsuleId && (
              <Link className="routine-fire-link" to={sessionPath(current.capsuleId)}>Open Capsule</Link>
            )}
          </>
        ) : (
          <span className="launch-hint">Has not run yet.</span>
        )}
        <span className={`routine-kept ${routine.keptCapsules >= routine.maxKept ? "full" : ""}`}>
          {routine.keptCapsules} of {routine.maxKept} kept
        </span>
        <span className="launch-hint">Stops after {durationText(routine.maxRunSeconds)}</span>
      </div>
      {routine.keptCapsules >= routine.maxKept && (
        <p className="routine-backlog">
          {routine.keptCapsules} runs are waiting for you. New runs are skipped until you delete one of their Capsules.
        </p>
      )}
      {!routine.enabled && off !== "Off" && <p className="routine-backlog">{off}</p>}
      {confirmDelete ? (
        <div className="routine-actions" role="group" aria-label="Confirm delete">
          <span>Delete {routine.name}? Capsules it kept stay.</span>
          <Button variant="danger" onClick={() => remove.mutate()} disabled={remove.isPending}>Delete</Button>
          <Button onClick={() => setConfirmDelete(false)}>Cancel</Button>
        </div>
      ) : (
        <div className="routine-actions">
          <Button onClick={() => run.mutate()} disabled={run.isPending || Boolean(routine.active)}>Run now</Button>
          <Button onClick={() => setEditing(true)}>Edit</Button>
          <Button onClick={() => toggle.mutate()} disabled={toggle.isPending}>
            {routine.enabled ? "Turn off" : "Turn on"}
          </Button>
          <Button onClick={() => setHistory((open) => !open)} aria-expanded={history}>
            {history ? "Hide runs" : "Runs"}
          </Button>
          <Button variant="danger" onClick={() => setConfirmDelete(true)}>Delete</Button>
        </div>
      )}
      {failure && <p role="alert">{normalizeAPIError(failure).message}</p>}
      {history && <FireHistory routineId={routine.id} />}
    </li>
  );
}

function structuredHarnesses(project: Project, catalog: HarnessImage[]) {
  return (project.harnessImages ?? [])
    .filter((pack) => packMayRunStructured(pack, catalog))
    .map((pack) => pack.name);
}

const intro =
  "Start a fresh Capsule with the same prompt on a schedule. A run with no changes deletes its Capsule. " +
  "A run with changes, a question, or a failure waits for you. A routine never ships on its own.";

function NewRoutine({
  projects, catalog, initialProject, onDone,
}: {
  projects: Project[];
  catalog: HarnessImage[];
  initialProject: string;
  onDone: () => void;
}) {
  const eligible = projects.filter((project) =>
    (project.state ?? "active") === "active" && structuredHarnesses(project, catalog).length > 0);
  const [projectId, setProjectId] = useState(
    eligible.some((project) => project.id === initialProject) ? initialProject : eligible[0]?.id ?? "",
  );
  const project = eligible.find((item) => item.id === projectId);
  if (!project) {
    return (
      <div className="routine-form">
        <p className="launch-hint">No Project has a harness that runs structured sessions yet. Add one in the Project&apos;s settings.</p>
        <div className="routine-form-actions"><Button onClick={onDone}>Cancel</Button></div>
      </div>
    );
  }
  return (
    <div className="routine-new">
      <label className="routine-field routine-project-field">
        Project
        <Select value={projectId} onChange={(event) => setProjectId(event.target.value)}>
          {eligible.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
        </Select>
      </label>
      <RoutineForm
        key={project.id}
        project={project}
        harnesses={structuredHarnesses(project, catalog)}
        onDone={onDone}
      />
    </div>
  );
}

/** Routines is the page that lists every routine you own, by Project. */
export function Routines({ embedded = false }: { embedded?: boolean }) {
  const [params, setParams] = useSearchParams();
  const only = params.get("project") ?? "";
  const capabilities = useQuery(queries.capabilities());
  const projects = useQuery(queries.projects());
  const [creating, setCreating] = useState(false);
  const structured = capabilities.data?.structured === true;
  const routines = useQuery({
    queryKey: allRoutinesKey,
    queryFn: ({ signal }) => api.allRoutines(signal),
    refetchInterval: 15_000,
    enabled: structured,
  });
  const catalog = capabilities.data?.harnessImages ?? [];
  const projectItems = projects.data?.items ?? [];
  const shown = only ? projectItems.filter((project) => project.id === only) : projectItems;
  const groups = shown
    .map((project) => ({
      project,
      items: (routines.data?.items ?? []).filter((routine) => routine.projectId === project.id),
    }))
    .filter((group) => group.items.length > 0);
  const filtered = only ? projectItems.find((project) => project.id === only) : undefined;
  const canCreate = structured && projectItems.some((project) => structuredHarnesses(project, catalog).length > 0);
  return (
    <article className={embedded ? "settings-embedded routines-page" : "workspace-detail routines-page"} aria-labelledby="routines-title">
      {embedded ? <p className="view-description">{intro}</p> : (
      <header className="workspace-view-header">
        <div>
          <p className="eyebrow">Automation</p>
          <h1 id="routines-title">Routines</h1>
          <p className="view-description">{intro}</p>
        </div>
        {canCreate && !creating && (
          <Button variant="primary" onClick={() => setCreating(true)}>
            <PlusIcon />
            New routine
          </Button>
        )}
      </header>
      )}
      {embedded && canCreate && !creating && (
        <Button variant="primary" onClick={() => setCreating(true)}>
          <PlusIcon />
          New routine
        </Button>
      )}
      {filtered && (
        <p className="routines-filter">
          Showing routines for <strong>{filtered.name}</strong>.{" "}
          <button type="button" className="text-button" onClick={() => setParams({})}>Show all</button>
        </p>
      )}
      {capabilities.isSuccess && !structured && (
        <p className="launch-hint">This Station cannot run structured sessions, so it cannot run routines.</p>
      )}
      {creating && (
        <NewRoutine
          projects={projectItems}
          catalog={catalog}
          initialProject={only}
          onDone={() => setCreating(false)}
        />
      )}
      {structured && (routines.isPending || projects.isPending) && <p role="status">Loading routines…</p>}
      {routines.isError && <p role="alert">{normalizeAPIError(routines.error).message}</p>}
      {groups.map(({ project, items }) => (
        <section key={project.id} className="routine-group" aria-labelledby={`routines-${project.id}`}>
          <h2 id={`routines-${project.id}`}>
            <Link to={`/ui/projects/${project.id}`}>{project.name}</Link>
          </h2>
          <ul className="routine-list">
            {items.map((routine) => (
              <RoutineCard
                key={routine.id}
                project={project}
                routine={routine}
                harnesses={structuredHarnesses(project, catalog)}
              />
            ))}
          </ul>
        </section>
      ))}
      {routines.isSuccess && projects.isSuccess && groups.length === 0 && !creating && (
        <div className="routine-empty">
          <p>No routines yet.</p>
          <p className="launch-hint">
            {canCreate
              ? "A weekly dependency bump or a nightly flaky-test pass are good first routines."
              : "Add a harness that runs structured sessions to a Project first."}
          </p>
        </div>
      )}
    </article>
  );
}

/** ProjectRoutines is the short pointer on a Project's settings page. */
export function ProjectRoutines({ project }: { project: Project }) {
  const capabilities = useQuery(queries.capabilities());
  const structured = capabilities.data?.structured === true;
  const routines = useQuery({
    queryKey: allRoutinesKey,
    queryFn: ({ signal }) => api.allRoutines(signal),
    enabled: structured,
  });
  if (!structured) return null;
  const count = routines.data?.items.filter((routine) => routine.projectId === project.id).length ?? 0;
  return (
    <section className="project-routines-section" aria-label="Routines">
      <div className="project-routines-head">
        <h2>Routines</h2>
        <Link className="button-link" to={`/ui/routines?project=${encodeURIComponent(project.id)}`}>
          {count > 0 ? "Manage routines" : "Set up a routine"}
        </Link>
      </div>
      <p className="launch-hint">
        {count === 0
          ? "No routines on this Project."
          : count === 1 ? "1 routine on this Project." : `${count} routines on this Project.`}
      </p>
    </section>
  );
}
