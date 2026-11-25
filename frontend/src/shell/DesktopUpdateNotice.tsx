import { Fragment, useCallback, useEffect, useRef, useState } from "react";

import { CheckIcon, CloseIcon, UpdateIcon } from "../components/ui/Icons";
import { Spinner } from "../components/ui/loading";
import { desktopBridge, type DesktopUpdateStatus } from "./desktop";
import {
  UPDATE_CHECK_EVERY_MS,
  UPDATE_DUE_EVENT,
  UPDATE_DUE_GAP_MS,
  UPDATE_LATER_KEY,
  UPDATE_PROGRESS_EVENT,
  UPDATE_TICK_MS,
  applyUpdateProgress,
  automaticCheckDue,
  laterRecord,
  recordUpdateCheck,
  updateBusyLabel,
  updateIsSnoozed,
  type UpdateProgress,
} from "./desktopUpdate";

type Offer =
  | { kind: "install"; version: string; busy: boolean; error: string }
  | { kind: "command"; version: string; command: string }
  | { kind: "note"; text: string };

function offerFromStatus(status: DesktopUpdateStatus, manual: boolean): Offer | null {
  if (!status.available || !status.version || !status.action) {
    if (!manual) return null;
    return { kind: "note", text: status.detail || "Meridian is up to date." };
  }
  if (
    !manual &&
    updateIsSnoozed(window.localStorage.getItem(UPDATE_LATER_KEY), status.version, Date.now())
  ) {
    return null;
  }
  if (status.action === "install") {
    return { kind: "install", version: status.version, busy: false, error: "" };
  }
  if (status.action === "command" && status.command) {
    return { kind: "command", version: status.version, command: status.command };
  }
  return manual ? { kind: "note", text: "Couldn't check for updates." } : null;
}

export function DesktopUpdateNotice() {
  const [offer, setOffer] = useState<Offer | null>(null);
  const [progress, setProgress] = useState<UpdateProgress | undefined>(undefined);
  const busyRef = useRef(false);
  const lastAutomatic = useRef<number | undefined>(undefined);

  const check = useCallback((manual: boolean) => {
    const bridge = desktopBridge();
    if (!bridge?.checkForUpdates) return;
    if (!manual) lastAutomatic.current = Date.now();
    void bridge.checkForUpdates().then(
      (status) => {
        recordUpdateCheck({ at: Date.now(), status });
        const next = offerFromStatus(status, manual);
        setOffer((current) => {
          // An install in flight owns the card until it restarts or fails.
          if (busyRef.current) return current;
          // An automatic check that finds nothing to offer leaves a manual
          // check's answer alone.
          if (!manual && !next) return current?.kind === "note" ? current : null;
          return next;
        });
      },
      () => {
        recordUpdateCheck({ at: Date.now(), status: null });
        if (manual && !busyRef.current) {
          setOffer({ kind: "note", text: "Couldn't check for updates." });
        }
      },
    );
  }, []);

  useEffect(() => {
    // The bridge is looked up on every check, so a bridge the shell installs
    // after this mounts still gets the shell's requests and the timer.
    const automatic = (gap: number) => {
      if (automaticCheckDue(lastAutomatic.current, Date.now(), gap)) check(false);
    };
    check(false);
    const timer = window.setInterval(() => automatic(UPDATE_CHECK_EVERY_MS), UPDATE_TICK_MS);
    const onManual = () => check(true);
    const onDue = () => automatic(UPDATE_DUE_GAP_MS);
    const onVisible = () => {
      if (document.visibilityState === "visible") automatic(UPDATE_CHECK_EVERY_MS);
    };
    const onProgress = (event: Event) => {
      if (!busyRef.current) return;
      const detail = event instanceof CustomEvent ? event.detail : undefined;
      setProgress((current) => applyUpdateProgress(current, detail));
    };
    window.addEventListener("meridian-check-updates", onManual);
    window.addEventListener(UPDATE_DUE_EVENT, onDue);
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener(UPDATE_PROGRESS_EVENT, onProgress);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("meridian-check-updates", onManual);
      window.removeEventListener(UPDATE_DUE_EVENT, onDue);
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener(UPDATE_PROGRESS_EVENT, onProgress);
    };
  }, [check]);

  if (!offer) return null;

  const dismiss = () => {
    busyRef.current = false;
    setOffer(null);
  };
  const later = (version: string) => {
    window.localStorage.setItem(UPDATE_LATER_KEY, JSON.stringify(laterRecord(version, Date.now())));
    dismiss();
  };

  if (offer.kind === "note") {
    return (
      <section className="update-card" role="status" aria-label="Updates">
        <div className="update-card-head">
          <span className="update-card-icon" aria-hidden="true">
            <CheckIcon />
          </span>
          <p className="update-card-title">{offer.text}</p>
          <button
            type="button"
            className="icon-button update-card-close"
            aria-label="Dismiss"
            onClick={dismiss}
          >
            <CloseIcon />
          </button>
        </div>
      </section>
    );
  }

  const install = () => {
    if (offer.kind !== "install") return;
    const bridge = desktopBridge();
    if (!bridge?.installUpdate) return;
    const version = offer.version;
    busyRef.current = true;
    setProgress(undefined);
    setOffer({ kind: "install", version, busy: true, error: "" });
    void bridge.installUpdate().then(
      () => undefined,
      () => {
        busyRef.current = false;
        setProgress(undefined);
        setOffer({
          kind: "install",
          version,
          busy: false,
          error: "The update could not be installed.",
        });
      },
    );
  };

  const busy = offer.kind === "install" && offer.busy;
  const busyLabel = updateBusyLabel(progress);
  const actionLabel = busy ? busyLabel : "Update & restart";

  return (
    <section className="update-card" role="status" aria-label="Update available">
      <div className="update-card-head">
        <span className="update-card-icon" aria-hidden="true">
          <UpdateIcon />
        </span>
        <div className="update-card-text">
          <p className="update-card-title">Update available</p>
          <p className="update-card-detail">Meridian {offer.version}</p>
        </div>
        <button
          type="button"
          className="icon-button update-card-close"
          aria-label="Remind me later"
          title="Remind me later"
          onClick={() => later(offer.version)}
        >
          <CloseIcon />
        </button>
      </div>
      {offer.kind === "command" ? (
        <code className="update-card-command">
          {offer.command.split(" ").map((word, index) => (
            <Fragment key={index}>
              {index > 0 ? " " : null}
              <span>{word}</span>
            </Fragment>
          ))}
        </code>
      ) : null}
      {offer.kind === "install" && offer.error ? (
        <p className="update-card-error">{offer.error}</p>
      ) : null}
      {offer.kind === "install" ? (
        <button
          type="button"
          className="update-card-action"
          disabled={busy}
          aria-busy={busy || undefined}
          aria-label={actionLabel}
          onClick={install}
        >
          <span className="update-card-action-faces">
            <span className={busy ? "is-offstage" : undefined} aria-hidden={busy || undefined}>
              Update & restart
            </span>
            <span className={busy ? undefined : "is-offstage"} aria-hidden={busy ? undefined : true}>
              <Spinner />
              {busyLabel}
            </span>
            <span className="is-offstage" aria-hidden="true">
              <Spinner />
              Downloading… 100%
            </span>
          </span>
        </button>
      ) : null}
    </section>
  );
}
