import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useContext, useEffect, useRef, useState } from "react";
import { useLocation } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { CoordinationGrant, NotesDelivery } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { Account } from "../components/Account";
import { HarnessSetups } from "../components/HarnessSetups";
import { rememberedHarness, rememberHarness } from "../components/NewProjectDialog";
import { notesDeliveryLabels } from "../components/notesText";
import { ProjectChecks } from "../components/ProjectChecks";
import { ProjectEnvironment } from "../components/ProjectEnvironment";
import { ProjectHarnesses } from "../components/ProjectHarnesses";
import { ProviderConnections, connectableHarnesses, connectionSupports } from "../components/ProviderConnections";
import { Routines } from "../components/Routines";
import { profileQuery } from "../components/WorkstationProfile";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { CopyButton } from "../components/ui/CopyButton";
import { Input, Select } from "../components/ui/Field";
import { HarnessMark } from "../components/ui/HarnessMark";
import { CheckIcon, UpdateIcon } from "../components/ui/Icons";
import { Spinner } from "../components/ui/loading";
import { harnessName } from "../shell/capsuleActivity";
import { desktopBridge, type DesktopUpdateStatus } from "../shell/desktop";
import {
  UPDATE_LATER_KEY,
  UPDATE_PROGRESS_EVENT,
  applyUpdateProgress,
  laterRecord,
  updateBusyLabel,
  type StoredLater,
  type UpdateProgress,
} from "../shell/desktopUpdate";
import { isApplePlatform } from "../shell/paletteCommands";
import { hasStationSwitcher, requestStationAction } from "../shell/stationSwitch";
import { SettingsNavContext, SettingsProjectContext } from "./projectContext";

const deliveryChoices: NotesDelivery[] = ["all_threads", "leads_only", "off"];
const pauseChoices = [
  { seconds: 60 * 60, label: "1 hour" },
  { seconds: 4 * 60 * 60, label: "4 hours" },
  { seconds: 24 * 60 * 60, label: "1 day" },
  { seconds: 3 * 24 * 60 * 60, label: "3 days" },
];
const cleanupChoices = [
  { seconds: 15 * 60, label: "15 minutes" },
  { seconds: 60 * 60, label: "1 hour" },
  { seconds: 4 * 60 * 60, label: "4 hours" },
  { seconds: 24 * 60 * 60, label: "1 day" },
  { seconds: 0, label: "Never" },
];

function readSnoozedUpdate(): StoredLater | null {
  try {
    const raw = window.localStorage.getItem(UPDATE_LATER_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<StoredLater>;
    if (typeof parsed.version === "string" && typeof parsed.until === "number" && parsed.until > Date.now()) {
      return { version: parsed.version, until: parsed.until };
    }
  } catch {
    // ignore invalid storage
  }
  return null;
}

function useLeadGrant() {
  const { pathname } = useLocation();
  const threadId = pathname.match(/\/threads\/([^/]+)/)?.[1] ?? "";
  const thread = useQuery({ ...queries.thread(threadId), enabled: threadId !== "" });
  const lead = Boolean(thread.data?.coordinationGrantId && !thread.data.parentThreadId);
  const grantId = lead ? thread.data?.coordinationGrantId ?? "" : "";
  const grant = useQuery({ ...queries.coordinationGrant(grantId), enabled: grantId !== "" });
  return { threadId, thread, grantId, grant };
}

function useGrantMutation(grantId: string, grant: CoordinationGrant | undefined) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      spawnApproval?: "auto" | "operator";
      maxLiveCapsules?: number;
      archiveAfterSeconds?: number;
      unattendedLimitSeconds?: number;
    }) => {
      if (!grant) throw new Error("Grant is not loaded");
      return api.updateCoordinationGrant(grant, body);
    },
    onSettled: async () => {
      if (grantId) await client.invalidateQueries({ queryKey: ["coordination-grant", grantId] });
    },
  });
}

export function AgentsLeadBanner() {
  const { thread, grantId, grant } = useLeadGrant();
  if (grantId && grant.data) {
    const label = thread.data ? `${harnessName(thread.data.harness)} lead` : "current lead Thread";
    return (
      <div className="settings-callout settings-callout--active" role="status">
        <Badge tone="ready">Active lead</Badge>
        <span>
          Editing coordination limits for <strong>{label}</strong>.
        </span>
      </div>
    );
  }
  return (
    <div className="settings-callout" role="note">
      <Badge>Defaults shown</Badge>
      <span>Lead limits apply per lead Thread. Open a lead Thread to edit these controls.</span>
    </div>
  );
}

export function SetupCheckSetting() {
  const bridge = desktopBridge();
  const client = useQueryClient();
  const check = useQuery({
    queryKey: ["desktop-setup-check"],
    queryFn: async () => (await bridge?.setupCheck?.()) !== false,
    enabled: Boolean(bridge?.setupCheck),
    retry: false,
  });
  if (!bridge?.setupCheck) {
    return <Badge>Desktop only</Badge>;
  }
  const checked = check.data !== false;
  return (
    <input
      type="checkbox"
      className="settings-switch"
      aria-label="Check this computer for setup changes"
      checked={checked}
      disabled={check.isPending}
      onChange={(event) => {
        const enabled = event.target.checked;
        client.setQueryData(["desktop-setup-check"], enabled);
        void bridge.setSetupCheck?.(enabled).catch(() => {
          client.setQueryData(["desktop-setup-check"], !enabled);
        });
      }}
    />
  );
}

export function GitIdentitySetting() {
  const cache = useQueryClient();
  const profile = useQuery(profileQuery);
  const [gitName, setGitName] = useState("");
  const [gitEmail, setGitEmail] = useState("");
  const [savedNotice, setSavedNotice] = useState(false);
  useEffect(() => {
    setGitName(profile.data?.gitName ?? "");
    setGitEmail(profile.data?.gitEmail ?? "");
  }, [profile.data?.gitName, profile.data?.gitEmail]);
  const save = useMutation({
    mutationFn: () => {
      const current = profile.data;
      if (!current) throw new Error("Profile is not loaded");
      return api.updateProfile({
        gitName: gitName.trim(),
        gitEmail: gitEmail.trim(),
        defaultConnections: current.defaultConnections,
        expectedResourceVersion: current.resourceVersion,
      });
    },
    onSuccess: (next) => {
      cache.setQueryData(["profile"], next);
      setSavedNotice(true);
    },
  });
  if (profile.isPending) return <p role="status">Loading your profile…</p>;
  if (profile.isError) return <p role="alert">{normalizeAPIError(profile.error).message}</p>;
  const dirty = gitName !== profile.data.gitName || gitEmail !== profile.data.gitEmail;
  const half = (gitName.trim() === "") !== (gitEmail.trim() === "");
  return (
    <form
      className="settings-stack"
      onSubmit={(event) => {
        event.preventDefault();
        setSavedNotice(false);
        save.mutate();
      }}
    >
      <div className="settings-form-grid">
        <label>
          Name
          <Input
            value={gitName}
            placeholder="Ada Lovelace"
            maxLength={320}
            onChange={(event) => {
              setSavedNotice(false);
              setGitName(event.target.value);
            }}
          />
        </label>
        <label>
          Email
          <Input
            type="email"
            value={gitEmail}
            placeholder="ada@example.com"
            maxLength={320}
            onChange={(event) => {
              setSavedNotice(false);
              setGitEmail(event.target.value);
            }}
          />
        </label>
      </div>
      <div className="settings-form-actions">
        <Button type="submit" variant="secondary" disabled={save.isPending || !dirty || half}>
          {save.isPending ? "Saving…" : "Save identity"}
        </Button>
        {half && <span className="settings-inline-hint">Enter both a name and an email.</span>}
        {savedNotice && !dirty && !save.isError && (
          <span className="settings-saved-pill" role="status">
            Saved
          </span>
        )}
      </div>
      {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
    </form>
  );
}

export function CapsuleGitHubAccessSetting() {
  const cache = useQueryClient();
  const profile = useQuery(profileQuery);
  const save = useMutation({
    mutationFn: (capsuleGitHubAccess: boolean) => {
      const current = profile.data;
      if (!current) throw new Error("Profile is not loaded");
      return api.updateProfile({
        gitName: current.gitName,
        gitEmail: current.gitEmail,
        capsuleGitHubAccess,
        defaultConnections: current.defaultConnections,
        expectedResourceVersion: current.resourceVersion,
      });
    },
    onSuccess: (next) => cache.setQueryData(["profile"], next),
  });
  if (profile.isPending) return <p role="status">Loading…</p>;
  if (profile.isError) return <p role="alert">{normalizeAPIError(profile.error).message}</p>;
  return (
    <div className="settings-toggle-wrap">
      <input
        type="checkbox"
        className="settings-switch"
        aria-label="Give Capsules a GitHub App token"
        checked={profile.data.capsuleGitHubAccess}
        disabled={save.isPending}
        onChange={(event) => save.mutate(event.target.checked)}
      />
      {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
    </div>
  );
}

export function DesktopAlertsSetting() {
  const bridge = desktopBridge();
  const [permission, setPermission] = useState<NotificationPermission | "unsupported">(
    typeof Notification === "undefined" ? "unsupported" : Notification.permission,
  );
  if (bridge) return <Badge tone="ready">Desktop alerts on</Badge>;
  if (permission === "unsupported") return <Badge>Unsupported</Badge>;
  if (permission === "granted") return <Badge tone="ready">Alerts on</Badge>;
  if (permission === "denied") return <Badge tone="failed">Alerts blocked</Badge>;
  return (
    <Button
      variant="secondary"
      onClick={() => {
        void Notification.requestPermission().then((result) => setPermission(result));
      }}
    >
      Notify me
    </Button>
  );
}

export function DefaultHarnessSetting() {
  const capabilities = useQuery(queries.capabilities());
  const packs = capabilities.data?.harnessImages ?? [];
  const [stored, setStored] = useState(rememberedHarness);
  const value = packs.some((pack) => pack.name === stored) ? stored : packs[0]?.name ?? "";
  if (capabilities.isPending) return <p role="status">Loading…</p>;
  if (packs.length === 0) return <Badge>No packs installed</Badge>;
  return (
    <Select
      aria-label="Default harness for a new project"
      value={value}
      onChange={(event) => {
        setStored(event.target.value);
        rememberHarness(event.target.value);
      }}
    >
      {packs.map((pack) => (
        <option key={pack.name} value={pack.name}>
          {harnessName(pack.name)}
        </option>
      ))}
    </Select>
  );
}

export function DefaultConnectionsSetting() {
  const cache = useQueryClient();
  const profile = useQuery(profileQuery);
  const connections = useQuery({ queryKey: ["provider-connections"], queryFn: api.providerConnections });
  const save = useMutation({
    mutationFn: (defaultConnections: Record<string, string>) => {
      const current = profile.data;
      if (!current) throw new Error("Profile is not loaded");
      return api.updateProfile({
        gitName: current.gitName,
        gitEmail: current.gitEmail,
        defaultConnections,
        expectedResourceVersion: current.resourceVersion,
      });
    },
    onSuccess: (next) => cache.setQueryData(["profile"], next),
  });
  if (profile.isPending || connections.isPending) return <p role="status">Loading connections…</p>;
  if (profile.isError) return <p role="alert">{normalizeAPIError(profile.error).message}</p>;
  const value = profile.data;
  const names = [...new Set([...connectableHarnesses, ...Object.keys(value.defaultConnections)])].sort();
  return (
    <div className="settings-stack">
      <div className="settings-connection-grid">
        {names.map((harness) => {
          const options = (connections.data?.items ?? []).filter(
            (item) => !item.revoked && connectionSupports(harness, item),
          );
          const current = value.defaultConnections[harness] ?? "";
          return (
            <label key={harness} className="settings-connection-field">
              <span className="settings-connection-name">
                <HarnessMark harness={harness} size="sm" />
                {harnessName(harness)}
              </span>
              <Select
                aria-label={`Default connection for ${harnessName(harness)}`}
                value={options.some((item) => item.id === current) ? current : ""}
                disabled={save.isPending}
                onChange={(event) => {
                  const next = { ...value.defaultConnections };
                  if (event.target.value) next[harness] = event.target.value;
                  else delete next[harness];
                  save.mutate(next);
                }}
              >
                <option value="">No default connection</option>
                {options.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </Select>
            </label>
          );
        })}
      </div>
      {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
    </div>
  );
}

export function HarnessSetupsSetting() {
  return <HarnessSetups embedded />;
}

export function ProviderConnectionsSetting() {
  return <ProviderConnections embedded />;
}

export function ProjectHarnessesSetting() {
  const project = useContext(SettingsProjectContext);
  if (!project) return <p>Create a project to add harness packs.</p>;
  return <ProjectHarnesses project={project} embedded />;
}

export function ProjectChecksSetting() {
  const project = useContext(SettingsProjectContext);
  if (!project) return <p>Create a project to edit checks.</p>;
  return <ProjectChecks projectId={project.id} embedded />;
}

export function ProjectEnvironmentSetting() {
  const project = useContext(SettingsProjectContext);
  if (!project) return <p>Create a project to edit its environment.</p>;
  return <ProjectEnvironment projectId={project.id} embedded />;
}

export function NotesDeliverySetting() {
  const project = useContext(SettingsProjectContext);
  const client = useQueryClient();
  const notes = useQuery({ ...queries.projectNotes(project?.id ?? ""), enabled: Boolean(project) });
  const save = useMutation({
    mutationFn: (choice: NotesDelivery) => {
      if (!project || !notes.data) throw new Error("Notes are not loaded");
      return api.setProjectNotesDelivery(project.id, choice, notes.data.settingsVersion);
    },
    onSettled: () => {
      if (project) void client.invalidateQueries({ queryKey: ["project-notes", project.id] });
    },
  });
  if (!project) return <Badge>No project</Badge>;
  if (notes.isPending) return <p role="status">Loading…</p>;
  if (notes.isError) return <p role="alert">{normalizeAPIError(notes.error).message}</p>;
  return (
    <div className="settings-stack">
      <Select
        aria-label="Deliver to"
        value={notes.data.notesDelivery}
        disabled={save.isPending}
        onChange={(event) => save.mutate(event.target.value as NotesDelivery)}
      >
        {deliveryChoices.map((choice) => (
          <option key={choice} value={choice}>
            {notesDeliveryLabels[choice]}
          </option>
        ))}
      </Select>
      {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
    </div>
  );
}

function LeadSelect({
  label,
  value,
  options,
  onChange,
  pending,
  error,
}: {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
  pending: boolean;
  error?: unknown;
}) {
  const { grantId, grant } = useLeadGrant();
  const disabled = !grantId || !grant.data || pending;
  return (
    <div className="settings-stack">
      <Select aria-label={label} value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)}>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </Select>
      {error ? <p role="alert">{normalizeAPIError(error).message}</p> : null}
    </div>
  );
}

export function LeadPauseSetting() {
  const { grantId, grant } = useLeadGrant();
  const save = useGrantMutation(grantId, grant.data);
  const current = String(grant.data?.unattendedLimitSeconds ?? 24 * 60 * 60);
  return (
    <LeadSelect
      label="Pause without you"
      value={pauseChoices.some((choice) => String(choice.seconds) === current) ? current : String(pauseChoices[2]?.seconds)}
      options={pauseChoices.map((choice) => ({ value: String(choice.seconds), label: choice.label }))}
      pending={save.isPending}
      error={save.isError ? save.error : undefined}
      onChange={(value) => save.mutate({ unattendedLimitSeconds: Number(value) })}
    />
  );
}

export function LeadAtOnceSetting() {
  const { grantId, grant } = useLeadGrant();
  const save = useGrantMutation(grantId, grant.data);
  const current = String(grant.data?.maxLiveCapsules ?? 4);
  return (
    <LeadSelect
      label="Run at once"
      value={current}
      options={Array.from({ length: 16 }, (_, index) => {
        const count = String(index + 1);
        return { value: count, label: count };
      })}
      pending={save.isPending}
      error={save.isError ? save.error : undefined}
      onChange={(value) => save.mutate({ maxLiveCapsules: Number(value) })}
    />
  );
}

export function LeadCleanupSetting() {
  const { grantId, grant } = useLeadGrant();
  const save = useGrantMutation(grantId, grant.data);
  const current = String(grant.data?.archiveAfterSeconds ?? 60 * 60);
  return (
    <LeadSelect
      label="Clean up finished subagents"
      value={cleanupChoices.some((choice) => String(choice.seconds) === current) ? current : "3600"}
      options={cleanupChoices.map((choice) => ({ value: String(choice.seconds), label: choice.label }))}
      pending={save.isPending}
      error={save.isError ? save.error : undefined}
      onChange={(value) => save.mutate({ archiveAfterSeconds: Number(value) })}
    />
  );
}

export function LeadApproveSetting() {
  const { grantId, grant } = useLeadGrant();
  const save = useGrantMutation(grantId, grant.data);
  const checked = grant.data ? grant.data.spawnApproval === "operator" : false;
  return (
    <div className="settings-toggle-wrap">
      <input
        type="checkbox"
        className="settings-switch"
        aria-label="Approve each spawn"
        checked={checked}
        disabled={!grantId || !grant.data || save.isPending}
        onChange={(event) => save.mutate({ spawnApproval: event.target.checked ? "operator" : "auto" })}
      />
      {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
    </div>
  );
}

export function RoutinesSetting() {
  return <Routines embedded />;
}

type UpdateViewState =
  | { kind: "idle" }
  | { kind: "checking" }
  | { kind: "up-to-date"; detail: string; checkedAt: string }
  | { kind: "available"; status: DesktopUpdateStatus; error: string }
  | { kind: "installing"; status: DesktopUpdateStatus }
  | { kind: "error"; detail: string };

export function CheckUpdatesSetting() {
  const bridge = desktopBridge();
  const capabilities = useQuery(queries.capabilities());
  const gateway = useQuery({
    queryKey: ["desktop-gateway"],
    queryFn: () => bridge!.gateway!(),
    enabled: Boolean(bridge?.gateway),
    retry: false,
  });
  const [state, setState] = useState<UpdateViewState>({ kind: "idle" });
  const [progress, setProgress] = useState<UpdateProgress | undefined>(undefined);
  const [snoozed, setSnoozed] = useState<StoredLater | null>(readSnoozedUpdate);
  const installingRef = useRef(false);

  useEffect(() => {
    const onProgress = (event: Event) => {
      if (!installingRef.current) return;
      const detail = event instanceof CustomEvent ? event.detail : undefined;
      setProgress((current) => applyUpdateProgress(current, detail));
    };
    window.addEventListener(UPDATE_PROGRESS_EVENT, onProgress);
    return () => window.removeEventListener(UPDATE_PROGRESS_EVENT, onProgress);
  }, []);

  const runCheck = () => {
    if (!bridge?.checkForUpdates) return;
    setState({ kind: "checking" });
    void bridge.checkForUpdates().then(
      (status) => {
        if (status.available && status.version && status.action) {
          setState({ kind: "available", status, error: "" });
          return;
        }
        const checkedAt = new Date().toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
        setState({
          kind: "up-to-date",
          detail: status.detail || "Meridian is up to date.",
          checkedAt,
        });
      },
      () => {
        setState({ kind: "error", detail: "Couldn't check for updates." });
      },
    );
  };

  const runInstall = (status: DesktopUpdateStatus) => {
    if (!bridge?.installUpdate) return;
    installingRef.current = true;
    setProgress(undefined);
    setState({ kind: "installing", status });
    void bridge.installUpdate().then(
      () => undefined,
      () => {
        installingRef.current = false;
        setProgress(undefined);
        setState({ kind: "available", status, error: "The update could not be installed." });
      },
    );
  };

  const snoozeVersion = (version: string) => {
    const record = laterRecord(version, Date.now());
    window.localStorage.setItem(UPDATE_LATER_KEY, JSON.stringify(record));
    setSnoozed(record);
    setState({ kind: "idle" });
  };

  const hasDesktopUpdater = Boolean(bridge?.checkForUpdates);

  return (
    <div className="settings-updates-panel">
      <section className="settings-update-hero" aria-label="Desktop updates">
        <div className="settings-update-hero-main">
          <span
            className={`settings-update-icon${state.kind === "up-to-date" ? " is-ok" : state.kind === "error" ? " is-error" : ""}`}
            aria-hidden="true"
          >
            {state.kind === "checking" || state.kind === "installing" ? (
              <Spinner />
            ) : state.kind === "up-to-date" ? (
              <CheckIcon />
            ) : (
              <UpdateIcon />
            )}
          </span>
          <div className="settings-update-copy">
            {!hasDesktopUpdater ? (
              <>
                <div className="settings-update-title-row">
                  <strong>Meridian Desktop</strong>
                  <Badge>Browser</Badge>
                </div>
                <p>Update checks and one-click installs run in the Meridian desktop app.</p>
              </>
            ) : state.kind === "checking" ? (
              <>
                <strong>Checking for updates…</strong>
                <p>Looking for a newer release of Meridian Desktop.</p>
              </>
            ) : state.kind === "up-to-date" ? (
              <>
                <div className="settings-update-title-row">
                  <strong>{state.detail}</strong>
                  <Badge tone="ready">Up to date</Badge>
                </div>
                <p role="status">Last checked at {state.checkedAt}.</p>
              </>
            ) : state.kind === "available" || state.kind === "installing" ? (
              <>
                <div className="settings-update-title-row">
                  <strong>Update available</strong>
                  <Badge tone="ready">Meridian {state.status.version}</Badge>
                </div>
                <p role="status">
                  {state.kind === "installing"
                    ? updateBusyLabel(progress)
                    : state.status.action === "command"
                      ? "Run the command below in your terminal to update this installation."
                      : `Meridian ${state.status.version} is ready to download and install.`}
                </p>
              </>
            ) : state.kind === "error" ? (
              <>
                <div className="settings-update-title-row">
                  <strong>Update check failed</strong>
                  <Badge tone="failed">Error</Badge>
                </div>
                <p role="alert">{state.detail}</p>
              </>
            ) : (
              <>
                <div className="settings-update-title-row">
                  <strong>Meridian Desktop</strong>
                  <Badge>Automatic checks on</Badge>
                </div>
                <p>
                  {snoozed
                    ? `Meridian ${snoozed.version} is snoozed until ${new Date(snoozed.until).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}.`
                    : "Automatic checks run in the background without interrupting your sessions."}
                </p>
              </>
            )}
          </div>
          {hasDesktopUpdater && (
            <div className="settings-update-actions">
              {state.kind === "available" && state.status.action === "install" && bridge?.installUpdate ? (
                <>
                  <Button variant="primary" onClick={() => runInstall(state.status)}>
                    Update & restart
                  </Button>
                  {state.status.version && (
                    <Button variant="secondary" onClick={() => snoozeVersion(state.status.version!)}>
                      Remind me later
                    </Button>
                  )}
                </>
              ) : state.kind === "installing" ? (
                <Button variant="primary" disabled aria-busy="true">
                  {updateBusyLabel(progress)}
                </Button>
              ) : state.kind === "available" && state.status.action === "command" ? (
                state.status.version ? (
                  <Button variant="secondary" onClick={() => snoozeVersion(state.status.version!)}>
                    Remind me later
                  </Button>
                ) : null
              ) : (
                <Button
                  variant={state.kind === "up-to-date" ? "secondary" : "primary"}
                  disabled={state.kind === "checking"}
                  onClick={runCheck}
                >
                  {state.kind === "checking"
                    ? "Checking…"
                    : state.kind === "up-to-date"
                      ? "Check again"
                      : state.kind === "error"
                        ? "Try again"
                        : "Check for updates"}
                </Button>
              )}
            </div>
          )}
        </div>

        {state.kind === "available" && state.status.action === "command" && state.status.command && (
          <div className="settings-update-command">
            <code>{state.status.command}</code>
            <CopyButton text={state.status.command} label="Copy update command" />
          </div>
        )}
        {state.kind === "available" && state.error && (
          <p className="settings-update-error" role="alert">
            {state.error}
          </p>
        )}
      </section>

      <section className="settings-meta-card" aria-label="Update schedule and runtime">
        <div className="settings-kv-row">
          <span className="settings-kv-key">Check schedule</span>
          <span className="settings-kv-val">On launch, every 4 hours, after wake, and when shown</span>
        </div>
        <div className="settings-kv-row">
          <span className="settings-kv-key">Installation</span>
          <span className="settings-kv-val">Always asks before downloading and restarting</span>
        </div>
        <div className="settings-kv-row">
          <span className="settings-kv-key">Remind me later</span>
          <span className="settings-kv-val">Hides that release for 24 hours</span>
        </div>
        {gateway.data && (
          <div className="settings-kv-row">
            <span className="settings-kv-key">Connected Station</span>
            <span className="settings-kv-val">
              {gateway.data.label}
              <Badge>{gateway.data.local ? "Local" : "Remote"}</Badge>
            </span>
          </div>
        )}
        {capabilities.data && (
          <div className="settings-kv-row">
            <span className="settings-kv-key">Capsule runtime</span>
            <span className="settings-kv-val">
              <code className="mono">{capabilities.data.providerVersion}</code>
              <Badge>{capabilities.data.isolation}</Badge>
            </span>
          </div>
        )}
      </section>
    </div>
  );
}

export function AccountSetting() {
  return <Account embedded />;
}

export function StationSetting() {
  const bridge = desktopBridge();
  if (!bridge?.openGatewaySettings && !hasStationSwitcher(bridge)) {
    return <Badge>Desktop only</Badge>;
  }
  return (
    <div className="settings-station-control">
      <Button variant="secondary" onClick={() => requestStationAction({ kind: "picker" })}>
        Switch or manage Stations…
      </Button>
      <span className="settings-kbd-hint">{isApplePlatform() ? "⌘1–⌘9" : "Ctrl+1–Ctrl+9"}</span>
    </div>
  );
}

export function TeamModeSetting() {
  const openSection = useContext(SettingsNavContext);
  const mode = useQuery(queries.authenticationMode());
  const connections = useQuery({ queryKey: ["provider-connections"], queryFn: api.providerConnections });
  const capabilities = useQuery(queries.capabilities());
  if (mode.isPending) return <p role="status">Loading…</p>;
  if (mode.isError) return <p role="alert">{normalizeAPIError(mode.error).message}</p>;
  const isTeam = mode.data.mode === "team";
  const gatewayState = connections.data ? (connections.data.enabled ? "on" : "off") : "loading";
  return (
    <div className="settings-meta-card">
      <div className="settings-kv-row">
        <span className="settings-kv-key">Authentication mode</span>
        <span className="settings-kv-val">
          <Badge tone={isTeam ? "ready" : undefined}>{isTeam ? "Team mode" : "Single-user"}</Badge>
          {mode.data.claimable && openSection && (
            <Button variant="secondary" onClick={() => openSection("account")}>
              Claim in Account
            </Button>
          )}
        </span>
      </div>
      <div className="settings-kv-row">
        <span className="settings-kv-key">Provider gateway</span>
        <span className="settings-kv-val">
          {gatewayState === "loading" ? (
            <span className="settings-inline-hint">Loading…</span>
          ) : (
            <Badge tone={gatewayState === "on" ? "ready" : undefined}>{gatewayState === "on" ? "On" : "Off"}</Badge>
          )}
        </span>
      </div>
      {capabilities.data && (
        <div className="settings-kv-row">
          <span className="settings-kv-key">Runtime isolation</span>
          <span className="settings-kv-val">
            <Badge>{capabilities.data.isolation}</Badge>
            <code className="mono">{capabilities.data.providerVersion}</code>
          </span>
        </div>
      )}
    </div>
  );
}
