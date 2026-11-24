import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useContext, useEffect, useState } from "react";
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
import { SetupChangeCheck, profileQuery } from "../components/WorkstationProfile";
import { Button } from "../components/ui/Button";
import { Input, Select } from "../components/ui/Field";
import { readTheme, rememberTheme, themeLabel, type ThemePreference } from "../components/ui/theme";
import { harnessName } from "../shell/capsuleActivity";
import { desktopBridge } from "../shell/desktop";
import { isApplePlatform } from "../shell/paletteCommands";
import { hasStationSwitcher, requestStationAction } from "../shell/stationSwitch";
import { SettingsProjectContext } from "./projectContext";

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

function useLeadGrant() {
  const { pathname } = useLocation();
  const threadId = pathname.match(/\/threads\/([^/]+)/)?.[1] ?? "";
  const thread = useQuery({ ...queries.thread(threadId), enabled: threadId !== "" });
  const lead = Boolean(thread.data?.coordinationGrantId && !thread.data.parentThreadId);
  const grantId = lead ? thread.data?.coordinationGrantId ?? "" : "";
  const grant = useQuery({ ...queries.coordinationGrant(grantId), enabled: grantId !== "" });
  return { grantId, grant };
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

export function SetupCheckSetting() {
  if (!desktopBridge()?.setupCheck) {
    return <p>Only the desktop app watches this computer for setup changes. It starts on.</p>;
  }
  return <SetupChangeCheck />;
}

export function GitIdentitySetting() {
  const cache = useQueryClient();
  const profile = useQuery(profileQuery);
  const [gitName, setGitName] = useState("");
  const [gitEmail, setGitEmail] = useState("");
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
    onSuccess: (next) => cache.setQueryData(["profile"], next),
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
        save.mutate();
      }}
    >
      <label>
        Name
        <Input value={gitName} maxLength={320} onChange={(event) => setGitName(event.target.value)} />
      </label>
      <label>
        Email
        <Input type="email" value={gitEmail} maxLength={320} onChange={(event) => setGitEmail(event.target.value)} />
      </label>
      <div>
        <Button type="submit" variant="secondary" disabled={save.isPending || !dirty || half}>
          Save identity
        </Button>
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
  if (profile.isPending) return <p role="status">Loading your profile…</p>;
  if (profile.isError) return <p role="alert">{normalizeAPIError(profile.error).message}</p>;
  return (
    <div className="settings-stack">
      <input
        type="checkbox"
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
  if (bridge) return <span>Desktop alerts on</span>;
  if (permission === "unsupported") return <span>This browser cannot show alerts.</span>;
  if (permission === "granted") return <span>Alerts on</span>;
  if (permission === "denied") return <span>Alerts blocked</span>;
  return (
    <Button
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
  if (capabilities.isPending) return <p role="status">Loading harness packs…</p>;
  if (packs.length === 0) return <p>No harness packs are installed.</p>;
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
      {names.map((harness) => {
        const options = (connections.data?.items ?? []).filter(
          (item) => !item.revoked && connectionSupports(harness, item),
        );
        const current = value.defaultConnections[harness] ?? "";
        return (
          <label key={harness}>
            {harnessName(harness)}
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
      {save.isError && <p role="alert">{normalizeAPIError(save.error).message}</p>}
    </div>
  );
}

export function HarnessSetupsSetting() {
  return <HarnessSetups embedded />;
}

export function ProviderConnectionsSetting() {
  return <ProviderConnections />;
}

export function ProjectHarnessesSetting() {
  const project = useContext(SettingsProjectContext);
  if (!project) return <p>Create a project to add harness packs.</p>;
  return <ProjectHarnesses project={project} />;
}

export function ProjectChecksSetting() {
  const project = useContext(SettingsProjectContext);
  if (!project) return <p>Create a project to edit checks.</p>;
  return <ProjectChecks projectId={project.id} />;
}

export function ProjectEnvironmentSetting() {
  const project = useContext(SettingsProjectContext);
  if (!project) return <p>Create a project to edit its environment.</p>;
  return <ProjectEnvironment projectId={project.id} />;
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
  if (!project) return <p>Create a project to choose who receives its notes.</p>;
  if (notes.isPending) return <p role="status">Loading notes…</p>;
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
    <div className="settings-stack">
      <input
        type="checkbox"
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

export function ThemeSetting() {
  const [theme, setTheme] = useState<ThemePreference>(readTheme);
  return (
    <div className="segmented-control" role="radiogroup" aria-label="Color theme">
      {(["system", "light", "dark"] as const).map((value) => (
        <button
          key={value}
          type="button"
          role="radio"
          aria-checked={theme === value}
          className={theme === value ? "active" : undefined}
          onClick={() => {
            setTheme(value);
            rememberTheme(value);
          }}
        >
          {themeLabel(value)}
        </button>
      ))}
    </div>
  );
}

export function CheckUpdatesSetting() {
  const bridge = desktopBridge();
  const [message, setMessage] = useState("");
  if (!bridge?.checkForUpdates) return <p>Update checks run in the desktop app.</p>;
  return (
    <div className="settings-stack">
      <div>
        <Button
          variant="primary"
          onClick={() => {
            void bridge.checkForUpdates?.().then(
              (status) => setMessage(status.detail || (status.available ? `Update ${status.version ?? ""}`.trim() : "Meridian is up to date.")),
              () => setMessage("Couldn't check for updates."),
            );
          }}
        >
          Check for updates
        </Button>
      </div>
      {message ? <p role="status">{message}</p> : null}
    </div>
  );
}

export function AccountSetting() {
  return <Account embedded />;
}

export function StationSetting() {
  const bridge = desktopBridge();
  if (!bridge?.openGatewaySettings && !hasStationSwitcher(bridge)) {
    return <p>Station switching is part of the desktop app.</p>;
  }
  return (
    <div className="settings-stack">
      <div>
        <Button onClick={() => requestStationAction({ kind: "picker" })}>Switch or manage Stations…</Button>
      </div>
      <p>{isApplePlatform() ? "⌘1–⌘9" : "Ctrl+1–Ctrl+9"} switch to the first nine saved Stations.</p>
    </div>
  );
}

export function TeamModeSetting() {
  const mode = useQuery(queries.authenticationMode());
  const connections = useQuery({ queryKey: ["provider-connections"], queryFn: api.providerConnections });
  if (mode.isPending) return <p role="status">Loading…</p>;
  if (mode.isError) return <p role="alert">{normalizeAPIError(mode.error).message}</p>;
  const gateway = connections.data ? (connections.data.enabled ? "On" : "Off") : "…";
  return (
    <div className="settings-stack">
      <p>Team mode: {mode.data.mode === "team" ? "On" : "Off"}{mode.data.claimable ? ". This installation can still be claimed from Account." : ""}</p>
      <p>Provider gateway: {gateway}</p>
    </div>
  );
}
