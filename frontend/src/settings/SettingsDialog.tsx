import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { queries } from "../api/queries";
import { CloseIcon, SearchIcon } from "../components/ui/Icons";
import { Select } from "../components/ui/Field";
import { isApplePlatform } from "../shell/paletteCommands";
import { AgentsLeadBanner } from "./controls";
import { SettingsNavContext, SettingsProjectContext } from "./projectContext";
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

const sectionsWithSaveNote = new Set<SettingsSectionId>(["general", "models", "projects", "agents"]);

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
  const { pathname } = useLocation();
  const routeProjectId = pathname.match(/^\/ui\/projects\/([^/]+)/)?.[1] ?? "";
  const projects = useQuery(queries.projects());
  const items = projects.data?.items.filter((project) => project.state !== "deleted") ?? [];
  const [projectId, setProjectId] = useState(routeProjectId);
  const project = items.find((item) => item.id === projectId) ?? items[0];
  const titleId = useId();
  const backdropRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const trimmed = query.trim();
  const filtered = trimmed ? filterSettings(query) : null;
  const matches = filtered ?? settingsInSection(section);
  const visibleSections = settingsSections.filter((item) =>
    filtered ? filtered.some((setting) => setting.section === item.id) : true,
  );
  const navigateSection = useCallback(
    (next: SettingsSectionId) => {
      setQuery("");
      onSection(next);
    },
    [onSection],
  );

  useEffect(() => {
    if (contentRef.current) contentRef.current.scrollTop = 0;
  }, [section]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      if (document.querySelector(".wb-drawer")) return;
      const backdrops = document.querySelectorAll(".dialog-backdrop");
      if (backdrops.length > 0 && backdrops[backdrops.length - 1] !== backdropRef.current) return;
      event.preventDefault();
      onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const showSaveNote = Boolean(trimmed) || sectionsWithSaveNote.has(section);

  return (
    <SettingsNavContext.Provider value={navigateSection}>
      <SettingsProjectContext.Provider value={project}>
        <div ref={backdropRef} className="dialog-backdrop" onMouseDown={onClose}>
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
                      aria-current={!trimmed && item.id === section ? "page" : undefined}
                      onClick={() => navigateSection(item.id)}
                    >
                      <Icon />
                      {item.label}
                    </button>
                  );
                })}
              </nav>
              <div ref={contentRef} className="settings-window-content">
                {matches.length === 0 ? (
                  <p className="settings-window-empty">No settings match “{trimmed}”.</p>
                ) : (
                  <SettingsResults
                    settings={matches}
                    searching={Boolean(trimmed)}
                    projectId={project?.id ?? ""}
                    projectRepo={project?.repositoryUrl}
                    projects={items.map((item) => ({ id: item.id, name: item.name }))}
                    onProject={setProjectId}
                  />
                )}
              </div>
            </div>
            {showSaveNote && (
              <p className="settings-window-save">
                Switches and menus save immediately. Forms with multiple fields keep their Save button.
              </p>
            )}
          </section>
        </div>
      </SettingsProjectContext.Provider>
    </SettingsNavContext.Provider>
  );
}

function SettingsResults({
  settings,
  searching,
  projectId,
  projectRepo,
  projects,
  onProject,
}: {
  settings: SettingDefinition[];
  searching: boolean;
  projectId: string;
  projectRepo?: string;
  projects: { id: string; name: string }[];
  onProject: (id: string) => void;
}) {
  const groups = settingsSections
    .map((section) => ({ section, settings: settings.filter((setting) => setting.section === section.id) }))
    .filter((group) => group.settings.length > 0);
  return (
    <div className="settings-window-inner">
      {groups.map((group) => (
        <div key={group.section.id} className="settings-section-group">
          {searching ? (
            <h2 className="settings-search-group">{group.section.label}</h2>
          ) : (
            <header className="settings-section-head">
              <h2>{group.section.label}</h2>
              <p>{group.section.description}</p>
            </header>
          )}
          {group.section.id === "projects" && (
            <ProjectPicker
              projectId={projectId}
              projectRepo={projectRepo}
              projects={projects}
              onProject={onProject}
            />
          )}
          {group.section.id === "agents" && <AgentsLeadBanner />}
          <div className="settings-rows">
            {group.settings.map((setting) => (
              <SettingRow key={setting.id} setting={setting} />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function ProjectPicker({
  projectId,
  projectRepo,
  projects,
  onProject,
}: {
  projectId: string;
  projectRepo?: string;
  projects: { id: string; name: string }[];
  onProject: (id: string) => void;
}) {
  if (projects.length === 0) {
    return (
      <div className="settings-callout" role="note">
        <span>No projects yet. Create a project to configure its harnesses, checks, and environment.</span>
      </div>
    );
  }
  return (
    <div className="settings-project-bar">
      <label className="settings-project-picker">
        <span>Project</span>
        <Select aria-label="Project" value={projectId} onChange={(event) => onProject(event.target.value)}>
          {projects.map((project) => (
            <option key={project.id} value={project.id}>
              {project.name}
            </option>
          ))}
        </Select>
      </label>
      {projectRepo && <span className="settings-project-repo mono">{projectRepo}</span>}
    </div>
  );
}

function SettingRow({ setting }: { setting: SettingDefinition }) {
  const Control = setting.Control;
  if (setting.chrome === "panel") {
    return (
      <div className="settings-window-row settings-window-row--panel" data-setting={setting.id}>
        <Control />
      </div>
    );
  }
  const layout = setting.layout ?? "stack";
  return (
    <div className={`settings-window-row settings-window-row--${layout}`} data-setting={setting.id}>
      <div className="settings-window-copy">
        <div className="settings-window-label">{setting.label}</div>
        {setting.hint ? <p className="settings-window-hint">{setting.hint}</p> : null}
      </div>
      <div className="settings-window-control">
        <Control />
      </div>
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
