import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { Project, Schedule, ScheduleFire } from "../api/generated/types.gen";
import { packMayRunStructured } from "../api/harnessPacks";
import { queries } from "../api/queries";
import { harnessName, sessionPath } from "../shell/capsuleActivity";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { Input, Select, TextArea } from "./ui/Field";
import { HarnessMark } from "./ui/HarnessMark";
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
  parseCadence,
  triggerLabel,
  weekdayNames,
  type CadenceMode,
} from "./scheduleText";

// Scheduled Project Threads (ADR 0057). A schedule starts a fresh Capsule
// with a fixed prompt on a clock. Runs with no changes delete their Capsule;
// the rest wait for the operator. A schedule never ships.

const scheduleKey = (projectId: string) => ["schedules", projectId];

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
};

function formFrom(schedule: Schedule | undefined, harness: string): FormState {
  if (!schedule) {
    return {
      name: "", harness, model: "", effort: "", prompt: "",
      mode: "weekly", time: "06:00", minute: "0", weekday: "1", custom: "0 6 * * 1",
      timeZone: browserTimeZone(), maxRunMinutes: "120", catchUpMinutes: "60", maxKept: "5",
    };
  }
  const parts = parseCadence(schedule.cadence);
  return {
    name: schedule.name, harness: schedule.harness, model: schedule.model ?? "",
    effort: schedule.effort ?? "", prompt: schedule.prompt, mode: parts.mode,
    time: clockTime(parts.hour, parts.minute), minute: String(parts.minute),
    weekday: String(parts.weekday), custom: schedule.cadence, timeZone: schedule.timeZone,
    maxRunMinutes: String(Math.round(schedule.maxRunSeconds / 60)),
    catchUpMinutes: String(Math.round(schedule.catchUpSeconds / 60)),
    maxKept: String(schedule.maxKept),
  };
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
      <label className="schedule-field">
        {label}
        <Input value={value} placeholder="Harness default" onChange={(event) => onChange(event.target.value)} />
        <span className="launch-hint">{hint}</span>
      </label>
    );
  }
  return (
    <label className="schedule-field">
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

function ScheduleForm({
  project, schedule, harnesses, onDone,
}: {
  project: Project;
  schedule?: Schedule;
  harnesses: string[];
  onDone: () => void;
}) {
  const client = useQueryClient();
  const [form, setForm] = useState<FormState>(() => formFrom(schedule, harnesses[0] ?? ""));
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
      };
      if (schedule) {
        return api.updateSchedule(schedule.id, { ...body, expectedResourceVersion: schedule.resourceVersion });
      }
      return api.createSchedule(project.id, {
        ...body, model: body.model || undefined, effort: body.effort || undefined,
      });
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: scheduleKey(project.id) });
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
    <form className="schedule-form" onSubmit={submit} aria-label={schedule ? `Edit ${schedule.name}` : "New schedule"}>
      <div className="schedule-form-grid">
        <label className="schedule-field">
          Name
          <Input
            value={form.name}
            placeholder="deps-weekly"
            maxLength={48}
            onChange={(event) => set({ name: event.target.value.toLowerCase() })}
          />
        </label>
        <label className="schedule-field">
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
      <label className="schedule-field">
        Prompt
        <TextArea
          rows={5}
          value={form.prompt}
          maxLength={32768}
          placeholder="Update minor and patch dependencies, run the tests, and propose a delivery if anything changed."
          onChange={(event) => set({ prompt: event.target.value })}
        />
      </label>
      <fieldset className="schedule-when-fields">
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
          <label className="schedule-inline">
            at minute
            <Input aria-label="Minute" inputMode="numeric" value={form.minute} onChange={(event) => set({ minute: event.target.value })} />
          </label>
        )}
        {(form.mode === "daily" || form.mode === "weekdays" || form.mode === "weekly") && (
          <label className="schedule-inline">
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
        <Input aria-label="Time zone" value={form.timeZone} onChange={(event) => set({ timeZone: event.target.value })} />
      </fieldset>
      <p className="launch-hint">
        <span className="mono">{cadence}</span> in {form.timeZone || "UTC"}. Runs must be at least 15 minutes apart.
      </p>
      <details className="schedule-limits">
        <summary>Limits</summary>
        <div className="schedule-form-grid">
          <label className="schedule-field">
            Stop a run after (minutes)
            <Input inputMode="numeric" value={form.maxRunMinutes} onChange={(event) => set({ maxRunMinutes: event.target.value })} />
          </label>
          <label className="schedule-field">
            Catch up a missed run within (minutes)
            <Input inputMode="numeric" value={form.catchUpMinutes} onChange={(event) => set({ catchUpMinutes: event.target.value })} />
          </label>
          <label className="schedule-field">
            Pause firing while this many runs wait for you
            <Input inputMode="numeric" value={form.maxKept} onChange={(event) => set({ maxKept: event.target.value })} />
          </label>
        </div>
      </details>
      {error && <p role="alert">{error}</p>}
      <div className="schedule-form-actions">
        <Button type="submit" variant="primary" disabled={save.isPending}>
          {schedule ? "Save schedule" : "Create schedule"}
        </Button>
        <Button onClick={onDone}>Cancel</Button>
      </div>
    </form>
  );
}

function FireRow({ fire }: { fire: ScheduleFire }) {
  const summary = fireSummary(fire);
  return (
    <li className="schedule-fire">
      <span className="schedule-fire-time">{formatWhen(fire.scheduledFor)}</span>
      <span className="schedule-fire-trigger">{triggerLabel(fire.trigger)}</span>
      <Badge tone={summary.tone}>{summary.label}</Badge>
      {fireKeptCapsule(fire) && fire.capsuleId && (
        <Link className="schedule-fire-link" to={sessionPath(fire.capsuleId)}>Open Capsule</Link>
      )}
    </li>
  );
}

function FireHistory({ scheduleId }: { scheduleId: string }) {
  const fires = useQuery({
    queryKey: ["schedule-fires", scheduleId],
    queryFn: ({ signal }) => api.scheduleFires(scheduleId, signal),
    refetchInterval: 15_000,
  });
  if (fires.isPending) return <p role="status">Loading runs…</p>;
  if (fires.isError) return <p role="alert">{normalizeAPIError(fires.error).message}</p>;
  if (fires.data.items.length === 0) return <p className="launch-hint">No runs yet.</p>;
  return (
    <ol className="schedule-fires" aria-label="Recent runs">
      {fires.data.items.map((fire) => <FireRow key={fire.id} fire={fire} />)}
    </ol>
  );
}

function ScheduleCard({
  project, schedule, harnesses,
}: {
  project: Project;
  schedule: Schedule;
  harnesses: string[];
}) {
  const client = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [history, setHistory] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: scheduleKey(project.id) });
    await client.invalidateQueries({ queryKey: ["schedule-fires", schedule.id] });
  };
  const toggle = useMutation({
    mutationFn: () => api.updateSchedule(schedule.id, {
      enabled: !schedule.enabled, expectedResourceVersion: schedule.resourceVersion,
    }),
    onSuccess: refresh,
  });
  const run = useMutation({ mutationFn: () => api.runSchedule(schedule.id), onSuccess: refresh });
  const remove = useMutation({
    mutationFn: () => api.deleteSchedule(schedule.id, schedule.resourceVersion),
    onSuccess: refresh,
  });
  const failure = [toggle, run, remove].find((mutation) => mutation.isError)?.error;
  if (editing) {
    return (
      <li className="schedule-card">
        <ScheduleForm project={project} schedule={schedule} harnesses={harnesses} onDone={() => setEditing(false)} />
      </li>
    );
  }
  const current = schedule.active ?? schedule.lastFire;
  const summary = current ? fireSummary(current) : undefined;
  const off = disabledText(schedule);
  return (
    <li className="schedule-card">
      <div className="schedule-card-head">
        <span className="schedule-name mono">{schedule.name}</span>
        <Badge tone={schedule.enabled ? "ready" : "paused"}>{schedule.enabled ? "On" : "Off"}</Badge>
        <span className="schedule-when">
          {describeCadence(schedule.cadence)} · {schedule.timeZone}
        </span>
      </div>
      <div className="schedule-meta">
        <span className="schedule-agent">
          <HarnessMark harness={schedule.harness} size="sm" />
          {harnessName(schedule.harness)}
          {schedule.model ? ` · ${schedule.model}` : " · default model"}
          {schedule.effort ? ` · ${schedule.effort} effort` : ""}
        </span>
        <span>
          {schedule.enabled && schedule.nextFireAt ? `Next: ${formatWhen(schedule.nextFireAt)}` : "Not scheduled"}
        </span>
      </div>
      <p className="schedule-prompt">{schedule.prompt}</p>
      <div className="schedule-status">
        {summary && current ? (
          <>
            <span className="launch-hint">{schedule.active ? "Now" : "Last run"}</span>
            <Badge tone={summary.tone}>{summary.label}</Badge>
            {fireKeptCapsule(current) && current.capsuleId && (
              <Link className="schedule-fire-link" to={sessionPath(current.capsuleId)}>Open Capsule</Link>
            )}
          </>
        ) : (
          <span className="launch-hint">Has not run yet.</span>
        )}
        <span className={`schedule-kept ${schedule.keptCapsules >= schedule.maxKept ? "full" : ""}`}>
          {schedule.keptCapsules} of {schedule.maxKept} kept
        </span>
        <span className="launch-hint">Stops after {durationText(schedule.maxRunSeconds)}</span>
      </div>
      {schedule.keptCapsules >= schedule.maxKept && (
        <p className="schedule-backlog">
          {schedule.keptCapsules} runs are waiting for you. New runs are skipped until you delete one of their Capsules.
        </p>
      )}
      {!schedule.enabled && off !== "Off" && <p className="schedule-backlog">{off}</p>}
      {confirmDelete ? (
        <div className="schedule-actions" role="group" aria-label="Confirm delete">
          <span>Delete {schedule.name}? Capsules it kept stay.</span>
          <Button variant="danger" onClick={() => remove.mutate()} disabled={remove.isPending}>Delete</Button>
          <Button onClick={() => setConfirmDelete(false)}>Cancel</Button>
        </div>
      ) : (
        <div className="schedule-actions">
          <Button onClick={() => run.mutate()} disabled={run.isPending || Boolean(schedule.active)}>Run now</Button>
          <Button onClick={() => setEditing(true)}>Edit</Button>
          <Button onClick={() => toggle.mutate()} disabled={toggle.isPending}>
            {schedule.enabled ? "Turn off" : "Turn on"}
          </Button>
          <Button onClick={() => setHistory((open) => !open)} aria-expanded={history}>
            {history ? "Hide runs" : "Runs"}
          </Button>
          <Button variant="danger" onClick={() => setConfirmDelete(true)}>Delete</Button>
        </div>
      )}
      {failure && <p role="alert">{normalizeAPIError(failure).message}</p>}
      {history && <FireHistory scheduleId={schedule.id} />}
    </li>
  );
}

export function ProjectSchedules({ project }: { project: Project }) {
  const capabilities = useQuery(queries.capabilities());
  const [creating, setCreating] = useState(false);
  const structured = capabilities.data?.structured === true;
  const schedules = useQuery({
    queryKey: scheduleKey(project.id),
    queryFn: ({ signal }) => api.schedules(project.id, signal),
    refetchInterval: 15_000,
    enabled: structured,
  });
  const catalog = capabilities.data?.harnessImages ?? [];
  const harnesses = (project.harnessImages ?? [])
    .filter((pack) => packMayRunStructured(pack, catalog))
    .map((pack) => pack.name);
  const active = (project.state ?? "active") === "active";
  return (
    <section className="project-schedules-section" aria-label="Schedules">
      <div className="project-schedules-head">
        <h2>Schedules</h2>
        {structured && active && harnesses.length > 0 && !creating && (
          <Button onClick={() => setCreating(true)}>New schedule</Button>
        )}
      </div>
      <p className="launch-hint">
        Start a fresh Capsule with the same prompt on a clock. A run with no changes deletes its Capsule.
        A run with changes, a question, or a failure waits for you. A schedule never ships on its own.
      </p>
      {!structured && <p className="launch-hint">This Station cannot run structured sessions, so it cannot run schedules.</p>}
      {structured && harnesses.length === 0 && (
        <p className="launch-hint">Add a harness pack that runs structured sessions to schedule work.</p>
      )}
      {creating && (
        <ScheduleForm project={project} harnesses={harnesses} onDone={() => setCreating(false)} />
      )}
      {structured && schedules.isPending && <p role="status">Loading schedules…</p>}
      {schedules.isError && <p role="alert">{normalizeAPIError(schedules.error).message}</p>}
      {schedules.data && schedules.data.items.length > 0 && (
        <ul className="schedule-list">
          {schedules.data.items.map((schedule) => (
            <ScheduleCard key={schedule.id} project={project} schedule={schedule} harnesses={harnesses} />
          ))}
        </ul>
      )}
      {schedules.data && schedules.data.items.length === 0 && !creating && structured && harnesses.length > 0 && (
        <p className="schedule-empty">No schedules yet.</p>
      )}
    </section>
  );
}
