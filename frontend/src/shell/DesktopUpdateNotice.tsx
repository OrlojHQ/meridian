import { useCallback, useEffect, useState } from "react";

import { desktopBridge, type DesktopUpdateStatus } from "./desktop";
import { UPDATE_LATER_KEY, laterRecord, updateIsSnoozed } from "./desktopUpdate";

const CHECK_EVERY_MS = 4 * 60 * 60 * 1000;

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

  const check = useCallback((manual: boolean) => {
    const bridge = desktopBridge();
    if (!bridge?.checkForUpdates) return;
    void bridge.checkForUpdates().then(
      (status) => setOffer(offerFromStatus(status, manual)),
      () => {
        if (manual) setOffer({ kind: "note", text: "Couldn't check for updates." });
      },
    );
  }, []);

  useEffect(() => {
    if (!desktopBridge()?.checkForUpdates) return;
    check(false);
    const timer = window.setInterval(() => check(false), CHECK_EVERY_MS);
    const onManual = () => check(true);
    window.addEventListener("meridian-check-updates", onManual);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("meridian-check-updates", onManual);
    };
  }, [check]);

  if (!offer) return null;

  const dismiss = () => setOffer(null);
  const later = (version: string) => {
    window.localStorage.setItem(UPDATE_LATER_KEY, JSON.stringify(laterRecord(version, Date.now())));
    dismiss();
  };

  if (offer.kind === "note") {
    return (
      <div className="desktop-update" role="status">
        <p>{offer.text}</p>
        <button type="button" className="secondary" onClick={dismiss}>
          OK
        </button>
      </div>
    );
  }

  const install = () => {
    if (offer.kind !== "install") return;
    const bridge = desktopBridge();
    if (!bridge?.installUpdate) return;
    const version = offer.version;
    setOffer({ kind: "install", version, busy: true, error: "" });
    void bridge.installUpdate().then(
      () => undefined,
      () =>
        setOffer({
          kind: "install",
          version,
          busy: false,
          error: "The update could not be installed.",
        }),
    );
  };

  return (
    <div className="desktop-update" role="status">
      <p>
        Meridian {offer.version} is available
        {offer.kind === "command" ? (
          <>
            . Run <code>{offer.command}</code>
          </>
        ) : null}
        {offer.kind === "install" && offer.error ? `. ${offer.error}` : null}
      </p>
      <div className="desktop-update-actions">
        {offer.kind === "install" ? (
          <button type="button" className="primary-button" disabled={offer.busy} onClick={install}>
            {offer.busy ? "Downloading…" : "Update & restart"}
          </button>
        ) : null}
        <button type="button" className="secondary" onClick={() => later(offer.version)}>
          Later
        </button>
      </div>
    </div>
  );
}
