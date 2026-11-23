// Mounts the Settings design mock. Development only. The production bundle
// does not import this module.

import { useState } from "react";
import { createRoot } from "react-dom/client";

import { SettingsDesign, type SettingsSectionId } from "./settingsMock";

const sectionIds: SettingsSectionId[] = [
  "general",
  "models",
  "projects",
  "agents",
  "routines",
  "appearance",
  "updates",
  "account",
  "advanced",
];

function readSection(value: string | null): SettingsSectionId {
  return sectionIds.find((id) => id === value) ?? "general";
}

function SettingsDesignRoot() {
  const params = new URLSearchParams(window.location.search);
  const [open, setOpen] = useState(params.get("open") !== "0");
  const [section, setSection] = useState<SettingsSectionId>(readSection(params.get("section")));
  const [query, setQuery] = useState(params.get("q") ?? "");
  return (
    <SettingsDesign
      open={open}
      section={section}
      query={query}
      onOpenChange={setOpen}
      onSection={setSection}
      onQuery={setQuery}
    />
  );
}

export function mountSettingsMock() {
  const host = document.createElement("div");
  host.id = "settings-design-mock";
  document.body.appendChild(host);
  createRoot(host).render(<SettingsDesignRoot />);
}
