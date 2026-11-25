import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import type { BuildInfo } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { CopyButton } from "../components/ui/CopyButton";
import { CheckIcon, UpdateIcon, WarningIcon } from "../components/ui/Icons";
import { Spinner } from "../components/ui/loading";
import { desktopBridge, type DesktopAppInfo, type DesktopUpdateStatus } from "../shell/desktop";
import {
  UPDATE_LATER_KEY,
  UPDATE_PROGRESS_EVENT,
  applyUpdateProgress,
  checkedAgo,
  isReleaseVersion,
  laterRecord,
  readLater,
  recordUpdateCheck,
  releaseNotesURL,
  updateBusyLabel,
  useLastUpdateCheck,
  type StoredLater,
  type UpdateProgress,
} from "../shell/desktopUpdate";

const installLabels: Record<DesktopAppInfo["install"], string> = {
  "mac-app": "macOS app",
  homebrew: "Homebrew",
  "linux-package": "Linux package",
};

function readSnoozed(): StoredLater | null {
  try {
    return readLater(window.localStorage.getItem(UPDATE_LATER_KEY), Date.now());
  } catch {
    return null;
  }
}

// The shell answers a failed check with available: false and a detail; an
// up-to-date answer carries no detail.
function checkFailed(status: DesktopUpdateStatus | null): string | null {
  if (!status) return "Couldn't check for updates.";
  if (!status.available && status.detail) return status.detail;
  return null;
}

function versionText(version: string) {
  return version === "0.0.0" || version === "dev" ? "Development build" : version;
}

function versionReport(
  app: DesktopAppInfo | undefined,
  build: BuildInfo | undefined,
  runtime: { providerVersion: string; isolation: string } | undefined,
) {
  const lines: string[] = [];
  if (app) lines.push(`Meridian Desktop ${app.version} (${installLabels[app.install]})`);
  if (build) lines.push(`meridiand ${build.version} (commit ${build.commit}, built ${build.date})`);
  if (runtime) lines.push(`Capsule runtime ${runtime.providerVersion} (${runtime.isolation})`);
  return lines.join("\n");
}

function useMinuteClock() {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, []);
  return now;
}

export function CheckUpdatesSetting() {
  const bridge = desktopBridge();
  const capabilities = useQuery(queries.capabilities());
  const gateway = useQuery({
    queryKey: ["desktop-gateway"],
    queryFn: () => bridge!.gateway!(),
    enabled: Boolean(bridge?.gateway),
    retry: false,
  });
  const appInfo = useQuery({
    queryKey: ["desktop-app-info"],
    queryFn: () => bridge!.appInfo!(),
    enabled: Boolean(bridge?.appInfo),
    retry: false,
    staleTime: Infinity,
  });
  const last = useLastUpdateCheck();
  const now = useMinuteClock();
  const [busy, setBusy] = useState<"checking" | "installing" | null>(null);
  const [installError, setInstallError] = useState("");
  const [progress, setProgress] = useState<UpdateProgress | undefined>(undefined);
  const [snoozed, setSnoozed] = useState<StoredLater | null>(readSnoozed);
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
    setBusy("checking");
    setInstallError("");
    void bridge.checkForUpdates().then(
      (status) => {
        recordUpdateCheck({ at: Date.now(), status });
        setBusy(null);
      },
      () => {
        recordUpdateCheck({ at: Date.now(), status: null });
        setBusy(null);
      },
    );
  };

  const runInstall = () => {
    if (!bridge?.installUpdate) return;
    installingRef.current = true;
    setProgress(undefined);
    setInstallError("");
    setBusy("installing");
    void bridge.installUpdate().then(
      () => undefined,
      () => {
        installingRef.current = false;
        setProgress(undefined);
        setBusy(null);
        setInstallError("The update could not be installed.");
      },
    );
  };

  const snooze = (version: string) => {
    const record = laterRecord(version, Date.now());
    try {
      window.localStorage.setItem(UPDATE_LATER_KEY, JSON.stringify(record));
    } catch {
      // Storage refused: the reminder simply comes back on the next check.
    }
    setSnoozed(record);
  };

  const unsnooze = () => {
    try {
      window.localStorage.removeItem(UPDATE_LATER_KEY);
    } catch {
      // Nothing stored to clear.
    }
    setSnoozed(null);
  };

  const hasDesktopUpdater = Boolean(bridge?.checkForUpdates);
  const app = appInfo.data;
  const build = capabilities.data?.build;
  const status = last?.status ?? null;
  const failure = last ? checkFailed(status) : null;
  const available = !failure && status?.available && status.version && status.action ? status : null;
  const offeredVersion = available?.version;
  const offerSnoozed = Boolean(offeredVersion && snoozed?.version === offeredVersion);
  const desktopTitle =
    app && isReleaseVersion(app.version)
      ? `Meridian Desktop ${app.version}`
      : app
        ? "Meridian Desktop (development build)"
        : "Meridian Desktop";
  // An offered release's notes say what updating brings; otherwise, what this
  // copy already has.
  const notesVersion = isReleaseVersion(offeredVersion)
    ? offeredVersion
    : isReleaseVersion(app?.version)
      ? app.version
    : isReleaseVersion(build?.version)
      ? build.version
      : undefined;
  const stationDiffers = Boolean(
    app && build && isReleaseVersion(app.version) && isReleaseVersion(build.version) && app.version !== build.version,
  );
  const report = versionReport(app, build, capabilities.data);

  type View = "browser" | "checking" | "installing" | "available" | "failed" | "current" | "unchecked";
  const view: View = !hasDesktopUpdater
    ? "browser"
    : busy === "checking"
      ? "checking"
      : busy === "installing" && available
        ? "installing"
        : available
          ? "available"
          : failure
            ? "failed"
            : last
              ? "current"
              : "unchecked";

  const icon =
    view === "checking" || view === "installing" ? (
      <Spinner />
    ) : view === "current" ? (
      <CheckIcon />
    ) : view === "failed" ? (
      <WarningIcon />
    ) : (
      <UpdateIcon />
    );

  return (
    <div className="settings-updates-panel">
      <section className="settings-update-hero" aria-label="Desktop updates">
        <div className="settings-update-hero-main">
          <span
            className={`settings-update-icon${view === "current" ? " is-ok" : view === "failed" ? " is-error" : ""}`}
            aria-hidden="true"
          >
            {icon}
          </span>
          <div className="settings-update-copy">
            {view === "browser" ? (
              <>
                <div className="settings-update-title-row">
                  <strong>Meridian Desktop</strong>
                  <Badge>Browser</Badge>
                </div>
                <p>Update checks and one-click installs run in the Meridian desktop app.</p>
              </>
            ) : view === "checking" ? (
              <>
                <strong>Checking for updates…</strong>
                <p role="status">Looking for a newer release of Meridian Desktop.</p>
              </>
            ) : view === "available" || view === "installing" ? (
              <>
                <div className="settings-update-title-row">
                  <strong>Update available</strong>
                  <Badge tone="ready">Meridian {offeredVersion}</Badge>
                </div>
                <p role="status">
                  {view === "installing"
                    ? updateBusyLabel(progress)
                    : offerSnoozed && snoozed
                      ? `Reminders for ${offeredVersion} are off until ${new Date(snoozed.until).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}.`
                      : available?.action === "command"
                        ? "Run the command below in your terminal to update this copy."
                        : `Meridian ${offeredVersion} is ready to download and install.`}
                </p>
              </>
            ) : view === "failed" ? (
              <>
                <div className="settings-update-title-row">
                  <strong>{desktopTitle}</strong>
                  <Badge tone="failed">Check failed</Badge>
                </div>
                <p role="alert">{failure}</p>
              </>
            ) : view === "current" ? (
              <>
                <div className="settings-update-title-row">
                  <strong>{desktopTitle}</strong>
                  <Badge tone="ready">Up to date</Badge>
                </div>
                <p role="status">{last ? `${checkedAgo(last.at, now)}.` : null}</p>
              </>
            ) : (
              <>
                <div className="settings-update-title-row">
                  <strong>{desktopTitle}</strong>
                </div>
                <p>Not checked yet. Meridian checks on launch, every 4 hours, and after wake.</p>
              </>
            )}
          </div>
          {hasDesktopUpdater && (
            <div className="settings-update-actions">
              {view === "installing" ? (
                <Button variant="primary" disabled aria-busy="true">
                  {updateBusyLabel(progress)}
                </Button>
              ) : view === "available" && available ? (
                <>
                  {available.action === "install" && bridge?.installUpdate ? (
                    <Button variant="primary" onClick={runInstall}>
                      Update & restart
                    </Button>
                  ) : null}
                  {offerSnoozed ? (
                    <Button variant="secondary" onClick={unsnooze}>
                      Turn reminders back on
                    </Button>
                  ) : (
                    <Button variant="secondary" onClick={() => snooze(available.version!)}>
                      Remind me later
                    </Button>
                  )}
                </>
              ) : (
                <Button
                  variant={view === "current" ? "secondary" : "primary"}
                  disabled={view === "checking"}
                  onClick={runCheck}
                >
                  {view === "checking"
                    ? "Checking…"
                    : view === "current"
                      ? "Check again"
                      : view === "failed"
                        ? "Try again"
                        : "Check for updates"}
                </Button>
              )}
            </div>
          )}
        </div>

        {view === "available" && available?.action === "command" && available.command && (
          <div className="settings-update-command">
            <code>{available.command}</code>
            <CopyButton text={available.command} label="Copy update command" />
          </div>
        )}
        {installError && (
          <p className="settings-update-error" role="alert">
            {installError}
          </p>
        )}
      </section>

      <section className="settings-meta-card" aria-label="Versions">
        {app && (
          <div className="settings-kv-row">
            <span className="settings-kv-key">Desktop app</span>
            <span className="settings-kv-val">
              <code className="mono">{versionText(app.version)}</code>
              <Badge>{installLabels[app.install]}</Badge>
            </span>
          </div>
        )}
        {gateway.data && (
          <div className="settings-kv-row">
            <span className="settings-kv-key">Station</span>
            <span className="settings-kv-val">
              {gateway.data.label}
              <Badge>{gateway.data.local ? "Local" : "Remote"}</Badge>
            </span>
          </div>
        )}
        {build && (
          <div className="settings-kv-row">
            <span className="settings-kv-key">Station version</span>
            <span className="settings-kv-val">
              <code className="mono" title={`commit ${build.commit}, built ${build.date}`}>
                {versionText(build.version)}
              </code>
              {stationDiffers && <Badge tone="paused">Differs from the desktop app</Badge>}
            </span>
          </div>
        )}
        {notesVersion && (
          <div className="settings-kv-row">
            <span className="settings-kv-key">Release notes</span>
            <span className="settings-kv-val">
              <a href={releaseNotesURL(notesVersion)} target="_blank" rel="noreferrer">
                What&rsquo;s new in {notesVersion}
              </a>
            </span>
          </div>
        )}
        {report && (
          <div className="settings-kv-row">
            <span className="settings-kv-key">For bug reports</span>
            <span className="settings-kv-val">
              <CopyVersionsButton text={report} />
            </span>
          </div>
        )}
      </section>

      {hasDesktopUpdater && (
        <p className="settings-update-footnote">
          Meridian checks on launch, every 4 hours, and after wake.{" "}
          {app && !app.selfUpdate
            ? `This copy is managed by ${app.install === "homebrew" ? "Homebrew" : "the Meridian installer"}, so Meridian shows the command to update it.`
            : "It always asks before downloading an update and restarting."}
        </p>
      )}
    </div>
  );
}

function CopyVersionsButton({ text }: { text: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setState("copied");
    } catch {
      setState("failed");
    }
  };
  return (
    <>
      <Button variant="secondary" onClick={() => void copy()}>
        {state === "copied" ? "Copied" : "Copy version info"}
      </Button>
      {state === "failed" && (
        <span className="settings-inline-hint" role="status">
          Clipboard access was refused.
        </span>
      )}
    </>
  );
}
