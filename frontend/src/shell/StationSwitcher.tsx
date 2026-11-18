import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";

import { ChevronIcon, PlusIcon, SettingsIcon } from "../components/ui/Icons";
import { MeridianMark } from "../components/ui/MeridianMark";
import { desktopBridge } from "./desktop";
import { isApplePlatform } from "./paletteCommands";
import { parseStations, requestStationSwitch } from "./stationSwitch";

function onlineOf(statuses: { id: string; online: boolean }[] | undefined, id: string) {
  const match = statuses?.find((status) => status.id === id);
  return match ? match.online : undefined;
}

export function StationSwitcher() {
  const bridge = desktopBridge();
  const stations = useQuery({
    queryKey: ["desktop-stations"],
    queryFn: async () => parseStations(await bridge?.stations?.()) ?? [],
    enabled: Boolean(bridge?.stations),
    staleTime: 15_000,
    retry: false,
  });
  const status = useQuery({
    queryKey: ["desktop-station-status"],
    queryFn: async () => {
      const value = await bridge?.stationStatus?.();
      if (!Array.isArray(value)) return [];
      return value.filter(
        (item): item is { id: string; online: boolean } =>
          Boolean(item) &&
          typeof item === "object" &&
          typeof (item as { id?: unknown }).id === "string" &&
          typeof (item as { online?: unknown }).online === "boolean",
      );
    },
    enabled: Boolean(bridge?.stationStatus) && Boolean(stations.data),
    staleTime: 15_000,
    retry: false,
  });
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const rows = stations.data ?? [];
  const current = rows.find((station) => station.current) ?? rows[0];
  const modifier = isApplePlatform() ? "⌘" : "Ctrl";

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

  if (!current) {
    return (
      <span className="brand">
        <span className="brand-mark">
          <MeridianMark />
        </span>
        <span>Meridian</span>
      </span>
    );
  }

  const online = onlineOf(status.data, current.id);
  return (
    <div className="station-switch" ref={root}>
      <button
        type="button"
        className={`station-switch-button${open ? " open" : ""}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={menuId}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="brand-mark">
          <MeridianMark />
        </span>
        <span className={`dot${online === true ? " online" : ""}${online === false ? " offline" : ""}`} />
        <strong>{current.name}</strong>
        <ChevronIcon className="chev" />
      </button>
      {open && (
        <div className="station-popover" id={menuId} role="menu" aria-label="Stations">
          <div className="group-label">Stations</div>
          {rows.map((station, index) => {
            const stationOnline = onlineOf(status.data, station.id);
            return (
              <button
                key={station.id}
                type="button"
                role="menuitem"
                className={`switch-row${station.current ? " current" : ""}`}
                onClick={() => {
                  setOpen(false);
                  if (!station.current) requestStationSwitch(station.id);
                }}
              >
                <span className="check" aria-hidden="true">
                  {station.current ? "✓" : ""}
                </span>
                <span
                  className={`dot${stationOnline === true ? " online" : ""}${stationOnline === false ? " offline" : ""}`}
                />
                <span className="name">{station.name}</span>
                <span className="host">
                  {station.host}
                  {stationOnline === false && <span className="offline-label"> Offline</span>}
                </span>
                {index < 9 && (
                  <span className="key-combo">
                    <kbd>{modifier}</kbd>
                    <kbd>{index + 1}</kbd>
                  </span>
                )}
              </button>
            );
          })}
          <div className="popover-actions">
            <button
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                void bridge?.openAddStation?.();
              }}
            >
              <PlusIcon />
              Add Station…
            </button>
            <button
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                void bridge?.openGatewaySettings?.();
              }}
            >
              <SettingsIcon />
              Manage Stations…
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
