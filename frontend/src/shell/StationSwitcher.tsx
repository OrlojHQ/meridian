import { useEffect, useId, useRef, useState } from "react";

import { ChevronIcon, PlusIcon, SettingsIcon } from "../components/ui/Icons";
import { MeridianMark } from "../components/ui/MeridianMark";
import { useDesktopGateway } from "./GatewayIndicator";
import { isApplePlatform } from "./paletteCommands";
import { requestStationAction } from "./stationSwitch";

// The sidebar names the open Station. Its menu opens the desktop app's own
// picker, where the saved Stations are listed with their status. The list
// is never given to a Station page (ADR 0055), so it is not shown here.
export function StationSwitcher() {
  const gateway = useDesktopGateway();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const modifier = isApplePlatform() ? "⌘" : "Ctrl+";

  useEffect(() => {
    if (!open) return;
    const onPointer = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    window.addEventListener("pointerdown", onPointer);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("pointerdown", onPointer);
      window.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const name = gateway.data?.label ?? "Meridian";
  return (
    <div className="station-switch" ref={root}>
      <button
        type="button"
        className={`station-switch-button${open ? " open" : ""}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={menuId}
        aria-label={`Station: ${name}`}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="brand-mark">
          <MeridianMark />
        </span>
        <strong>{name}</strong>
        <ChevronIcon className="chev" />
      </button>
      {open && (
        <div className="station-popover" id={menuId} role="menu" aria-label="Stations">
          <div className="popover-actions">
            <button
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                requestStationAction({ kind: "picker" });
              }}
            >
              <SettingsIcon />
              Switch or manage Stations…
            </button>
            <button
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                requestStationAction({ kind: "add" });
              }}
            >
              <PlusIcon />
              Add Station…
            </button>
          </div>
          <p className="station-popover-hint">
            {modifier}1–{modifier}9 switch to your first nine saved Stations.
          </p>
        </div>
      )}
    </div>
  );
}
