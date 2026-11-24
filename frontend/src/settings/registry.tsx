import type { ReactNode } from "react";

import {
  AccountIcon,
  FilesIcon,
  GatewayIcon,
  GearIcon,
  RoutineIcon,
  ThemeIcon,
  ThreadIcon,
  ToolIcon,
  UpdateIcon,
} from "../components/ui/Icons";
import {
  AccountSetting,
  CheckUpdatesSetting,
  DefaultConnectionsSetting,
  DefaultHarnessSetting,
  DesktopAlertsSetting,
  GitIdentitySetting,
  HarnessSetupsSetting,
  LeadApproveSetting,
  LeadAtOnceSetting,
  LeadCleanupSetting,
  LeadPauseSetting,
  NotesDeliverySetting,
  ProjectChecksSetting,
  ProjectEnvironmentSetting,
  ProjectHarnessesSetting,
  ProviderConnectionsSetting,
  RoutinesSetting,
  SetupCheckSetting,
  StationSetting,
  TeamModeSetting,
  ThemeSetting,
} from "./controls";

export type SettingsSectionId =
  | "general"
  | "models"
  | "projects"
  | "agents"
  | "routines"
  | "appearance"
  | "updates"
  | "account"
  | "advanced";

export type SettingDefinition = {
  id: string;
  section: SettingsSectionId;
  label: string;
  keywords: string;
  hint?: string;
  /** panel: the control draws its own layout. fields: label, hint, then the control. */
  chrome: "fields" | "panel";
  Control: () => ReactNode;
};

export const settingsSections: {
  id: SettingsSectionId;
  label: string;
  icon: typeof GearIcon;
}[] = [
  { id: "general", label: "General", icon: GearIcon },
  { id: "models", label: "Models & Harnesses", icon: ToolIcon },
  { id: "projects", label: "Projects", icon: FilesIcon },
  { id: "agents", label: "Agents", icon: ThreadIcon },
  { id: "routines", label: "Routines", icon: RoutineIcon },
  { id: "appearance", label: "Appearance", icon: ThemeIcon },
  { id: "updates", label: "Updates", icon: UpdateIcon },
  { id: "account", label: "Account", icon: AccountIcon },
  { id: "advanced", label: "Advanced", icon: GatewayIcon },
];

export function isSettingsSection(value: string | null): value is SettingsSectionId {
  return settingsSections.some((section) => section.id === value);
}

export function sectionLabel(id: SettingsSectionId) {
  return settingsSections.find((section) => section.id === id)?.label ?? id;
}

// One list. A new setting is a new entry here and nowhere else.
export const settingsRegistry: SettingDefinition[] = [
  {
    id: "setup-check",
    section: "general",
    label: "Check this computer for setup changes",
    keywords: "desktop import laptop harness git",
    hint: "On in the desktop app. A browser has nothing to watch, so this stays hidden there.",
    chrome: "panel",
    Control: SetupCheckSetting,
  },
  {
    id: "git-identity",
    section: "general",
    label: "Git commit identity",
    keywords: "name email git commit author",
    hint: "Agents commit as this. Empty until you set both the name and the email. SSH keys are not kept.",
    chrome: "fields",
    Control: GitIdentitySetting,
  },
  {
    id: "desktop-alerts",
    section: "general",
    label: "Desktop alerts",
    keywords: "notifications notify alerts",
    hint: 'A count and "Meridian needs you". Nothing is sent to a phone. The desktop app always leaves alerts on.',
    chrome: "fields",
    Control: DesktopAlertsSetting,
  },
  {
    id: "default-harness",
    section: "general",
    label: "Default harness for a new project",
    keywords: "pack remembered last",
    hint: "The last pack you chose. With nothing remembered, the first installed pack is selected.",
    chrome: "fields",
    Control: DefaultHarnessSetting,
  },
  {
    id: "default-connections",
    section: "models",
    label: "Default connection",
    keywords: "provider profile harness claude codex opencode pi deepseek",
    hint: "Preselected at launch. Each Project still asks once before it uses your login. Default is no connection.",
    chrome: "fields",
    Control: DefaultConnectionsSetting,
  },
  {
    id: "harness-setups",
    section: "models",
    label: "Harness setups",
    keywords: "default setup import files skills use by default",
    hint: "New Capsules start from the setup marked default. Existing Capsules keep the revision they started with.",
    chrome: "fields",
    Control: HarnessSetupsSetting,
  },
  {
    id: "provider-connections",
    section: "models",
    label: "Provider connections",
    keywords: "api key sign in endpoint model base url plain http",
    hint: "Sign in or save a key once, then authorize it for each Project.",
    chrome: "panel",
    Control: ProviderConnectionsSetting,
  },
  {
    id: "project-harnesses",
    section: "projects",
    label: "Harness packs",
    keywords: "agents add pack project",
    hint: "Packs on the project selected above. Adding one offers it for the next Capsule.",
    chrome: "fields",
    Control: ProjectHarnessesSetting,
  },
  {
    id: "project-checks",
    section: "projects",
    label: "Checks",
    keywords: "command timeout rework attempts automatically send failures",
    hint: "Run inside the Capsule when the agent finishes. Failures go back to the agent when that box is on. Attempts start at 3. A blank check times out after 600 seconds.",
    chrome: "fields",
    Control: ProjectChecksSetting,
  },
  {
    id: "project-environment",
    section: "projects",
    label: "Project environment",
    keywords: "setup script reuse prepared preparation",
    hint: "Reuse of a matching prepared environment starts on. The setup script starts empty. Save applies to future Capsules.",
    chrome: "fields",
    Control: ProjectEnvironmentSetting,
  },
  {
    id: "notes-delivery",
    section: "projects",
    label: "Deliver to",
    keywords: "project notes every thread leads only off",
    hint: "Copied as files into each new session. Routines opt in one by one. Default is Every Thread.",
    chrome: "fields",
    Control: NotesDeliverySetting,
  },
  {
    id: "lead-pause",
    section: "agents",
    label: "Pause without you",
    keywords: "unattended lead subagent",
    hint: "The lead stops starting subagents after this long without a message from you. Default is 1 day. Open a lead Thread to change that lead.",
    chrome: "fields",
    Control: LeadPauseSetting,
  },
  {
    id: "lead-at-once",
    section: "agents",
    label: "Run at once",
    keywords: "max live capsules subagent",
    hint: "Subagents the lead can run at a time. Default is 4, and the most is 16.",
    chrome: "fields",
    Control: LeadAtOnceSetting,
  },
  {
    id: "lead-cleanup",
    section: "agents",
    label: "Clean up finished subagents",
    keywords: "archive idle",
    hint: "Deletes a done subagent's Capsule after this long idle, unless it has unsaved changes. Default is 1 hour.",
    chrome: "fields",
    Control: LeadCleanupSetting,
  },
  {
    id: "lead-approve",
    section: "agents",
    label: "Approve each spawn",
    keywords: "spawn approval operator auto",
    hint: "Off, the lead starts subagents on its own. That is the default.",
    chrome: "fields",
    Control: LeadApproveSetting,
  },
  {
    id: "routines",
    section: "routines",
    label: "Routines",
    keywords: "schedule repeat weekly cadence model effort notes limits prompt name timezone",
    hint: "A new routine is on, weekly, Monday at 06:00 in this computer's time zone. Model and effort start empty. Project notes stay off. A run stops after 120 minutes, catches up within 60, and pauses after 5 runs are waiting.",
    chrome: "panel",
    Control: RoutinesSetting,
  },
  {
    id: "theme",
    section: "appearance",
    label: "Color theme",
    keywords: "light dark system appearance",
    hint: "System follows this computer. The choice is remembered on this browser.",
    chrome: "fields",
    Control: ThemeSetting,
  },
  {
    id: "check-updates",
    section: "updates",
    label: "Check for updates",
    keywords: "upgrade restart later desktop",
    hint: "The desktop app checks on launch, every four hours, after the computer wakes, and when the window is shown again. Nothing installs until you confirm. Remind me later hides that version for 24 hours.",
    chrome: "fields",
    Control: CheckUpdatesSetting,
  },
  {
    id: "account",
    section: "account",
    label: "Account",
    keywords: "signed in email role sign out github sessions token read-only members claim",
    hint: "Sign-in, GitHub, sessions, personal access tokens, and members. A new token expires after 90 days and is not read-only unless you check that box.",
    chrome: "panel",
    Control: AccountSetting,
  },
  {
    id: "station",
    section: "advanced",
    label: "Station",
    keywords: "gateway switch manage shortcuts",
    hint: "Opens the desktop list of saved Stations. The first nine use the number keys with the same modifier as the command palette.",
    chrome: "fields",
    Control: StationSetting,
  },
  {
    id: "team-mode",
    section: "advanced",
    label: "Team mode",
    keywords: "claim installation provider gateway",
    hint: "Offered once, before the installation is claimed, and it cannot be undone. Reusable provider connections need the provider gateway, which an administrator sets on the daemon.",
    chrome: "fields",
    Control: TeamModeSetting,
  },
];

export function filterSettings(query: string, source: SettingDefinition[] = settingsRegistry) {
  const needle = query.trim().toLowerCase();
  if (!needle) return source;
  return source.filter((setting) =>
    `${setting.label} ${setting.hint ?? ""} ${setting.keywords} ${sectionLabel(setting.section)}`
      .toLowerCase()
      .includes(needle),
  );
}

export function settingsInSection(section: SettingsSectionId, source: SettingDefinition[] = settingsRegistry) {
  return source.filter((setting) => setting.section === section);
}
