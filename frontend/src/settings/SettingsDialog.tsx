import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { queries } from "../api/queries";
import { CloseIcon, SearchIcon } from "../components/ui/Icons";
import { Select } from "../components/ui/Field";
import { isApplePlatform } from "../shell/paletteCommands";
import { SettingsProjectContext } from "./projectContext";
import {
  filterSettings,
  isSettingsSection,
  sectionLabel,
  settingsInSection,
  settingsSections,
  type SettingsSectionId,
  type SettingDefinition,
} from "./registry";
import "./settings.css";

export function settingsShortcutKeys() {
  return isApplePlatform() ? ["⌘", ","] : ["Ctrl", ","];
}

export function openSettingsSearch(search: string, section: SettingsSectionId) {
  const params = new URLSearchParams(search);
  params.set("settings", section);
  return `?${params.toString()}`;
}

export function closeSettingsSearch(search: string) {
  const params = new URLSearchParams(search);
  params.delete("settings");
  const next = params.toString();
  return next ? `?${next}` : "";
}

export function SettingsDialog({
  section,
  onSection,
  onClose,
}: {
  section: SettingsSectionId;
  onSection: (section: SettingsSectionId) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const projects = useQuery(queries.projects());
  const items = projects.data?.items.filter((project) => project.state !== "deleted") ?? [];
  const [projectId, setProjectId] = useState("");
  const project = items.find((item) => item.id === projectId) ?? items[0];
  const titleId = useId();
  const matches = query.trim() ? filterSettings(query) : settingsInSection(section);
  const visibleSections = settingsSections.filter((item) =>
    query.trim() ? filterSettings(query).some((setting) => setting.section === item.id) : true,
  );

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      if (document.querySelector(".wb-drawer")) return;
      event.preventDefault();
      onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <SettingsProjectContext.Provider value={project}>
      <div className="dialog-backdrop" onMouseDown={onClose}>
        <section
          className="settings-window"
          role="dialog"
          aria-modal="true"
          aria-labelledby={titleId}
          onMouseDown={(event) => event.stopPropagation()}
        >
          <header className="settings-window-top">
            <h1 id={titleId}>Settings</h1>
            <label className="activity-filter settings-window-search">
              <SearchIcon />
              <span className="sr-only">Search settings</span>
              <input
                value={query}
                placeholder="Search settings"
                onChange={(event) => setQuery(event.target.value)}
              />
            </label>
            <button type="button" className="icon-button" aria-label="Close settings" onClick={onClose}>
              <CloseIcon />
            </button>
          </header>
          <div className="settings-window-body">
            <nav className="settings-window-nav" aria-label="Settings sections">
              {visibleSections.map((item) => {
                const Icon = item.icon;
                return (
                  <button
                    key={item.id}
                    type="button"
                    aria-current={!query.trim() && item.id === section ? "page" : undefined}
                    onClick={() => {
                      setQuery("");
                      onSection(item.id);
                    }}
                  >
                    <Icon />
                    {item.label}
                  </button>
                );
              })}
            </nav>
            <div className="settings-window-content">
              {matches.length === 0 ? (
                <p className="settings-window-empty">No settings match “{query.trim()}”.</p>
              ) : (
                <SettingsResults
                  settings={matches}
                  query={query}
                  projectId={project?.id ?? ""}
                  projects={items.map((item) => ({ id: item.id, name: item.name }))}
                  onProject={setProjectId}
                />
              )}
            </div>
          </div>
          <p className="settings-window-save">Switches and menus save immediately. A form that collects several fields keeps its Save button.</p>
        </section>
      </div>
    </SettingsProjectContext.Provider>
  );
}

function SettingsResults({
  settings,
  query,
  projectId,
  projects,
  onProject,
}: {
  settings: SettingDefinition[];
  query: string;
  projectId: string;
  projects: { id: string; name: string }[];
  onProject: (id: string) => void;
}) {
  const groups = settingsSections
    .map((section) => ({ section, settings: settings.filter((setting) => setting.section === section.id) }))
    .filter((group) => group.settings.length > 0);
  return (
    <>
      {groups.map((group) => (
        <div key={group.section.id}>
          {query.trim() ? <h2>{group.section.label}</h2> : null}
          {group.section.id === "projects" && (
            <ProjectPicker projectId={projectId} projects={projects} onProject={onProject} />
          )}
          {group.settings.map((setting) => (
            <SettingRow key={setting.id} setting={setting} />
          ))}
        </div>
      ))}
    </>
  );
}

function ProjectPicker({
  projectId,
  projects,
  onProject,
}: {
  projectId: string;
  projects: { id: string; name: string }[];
  onProject: (id: string) => void;
}) {
  if (projects.length === 0) return <p>No projects yet.</p>;
  return (
    <div className="settings-project">
      <span>Project</span>
      <Select aria-label="Project" value={projectId} onChange={(event) => onProject(event.target.value)}>
        {projects.map((project) => (
          <option key={project.id} value={project.id}>
            {project.name}
          </option>
        ))}
      </Select>
    </div>
  );
}

function SettingRow({ setting }: { setting: SettingDefinition }) {
  const Control = setting.Control;
  return (
    <div className="settings-window-row" data-setting={setting.id}>
      {setting.chrome === "fields" ? (
        <>
          <div className="settings-window-label">{setting.label}</div>
          {setting.hint ? <p>{setting.hint}</p> : null}
          <div className="settings-window-control">
            <Control />
          </div>
        </>
      ) : (
        <Control />
      )}
    </div>
  );
}

export function useSettingsSection(): SettingsSectionId | null {
  const { search } = useLocation();
  const value = new URLSearchParams(search).get("settings");
  if (!value) return null;
  return isSettingsSection(value) ? value : "general";
}

export function SettingsRedirect({ section }: { section: SettingsSectionId }) {
  const location = useLocation();
  const navigate = useNavigate();
  useEffect(() => {
    const params = new URLSearchParams(location.search);
    params.set("settings", section);
    navigate({ pathname: "/ui/", search: `?${params.toString()}`, hash: location.hash }, { replace: true });
  }, [location.hash, location.search, navigate, section]);
  return null;
}

export { sectionLabel };
