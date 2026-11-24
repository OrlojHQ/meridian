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
  CapsuleGitHubAccessSetting,
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

export type SettingLayout = "split" | "stack" | "wide";

export type SettingDefinition = {
  id: string;
  section: SettingsSectionId;
  label: string;
  keywords: string;
  hint?: string;
  /** panel: the control draws its own layout. fields: label, hint, then the control. */
  chrome: "fields" | "panel";
  /** Row layout when chrome is "fields". */
  layout?: SettingLayout;
  Control: () => ReactNode;
};

export const settingsSections: {
  id: SettingsSectionId;
  label: string;
  description: string;
  icon: typeof GearIcon;
}[] = [
  {
    id: "general",
    label: "General",
    description: "Workstation sync, git commit identity, alerts, and project defaults.",
    icon: GearIcon,
  },
  {
    id: "models",
    label: "Models & Harnesses",
    description: "Connect model providers, pick per-harness defaults, and manage harness setups.",
    icon: ToolIcon,
  },
  {
    id: "projects",
    label: "Projects",
    description: "Configure harness packs, automated checks, preparation scripts, and notes per project.",
    icon: FilesIcon,
  },
  {
    id: "agents",
    label: "Agents",
    description: "Control how a lead agent spawns, limits, and cleans up subagents.",
    icon: ThreadIcon,
  },
  {
    id: "routines",
    label: "Routines",
    description: "Schedule recurring structured sessions on a cadence.",
    icon: RoutineIcon,
  },
  {
    id: "appearance",
    label: "Appearance",
    description: "Choose how Meridian looks on this device.",
    icon: ThemeIcon,
  },
  {
    id: "updates",
    label: "Updates",
    description: "Check for desktop app updates, install releases, and view runtime details.",
    icon: UpdateIcon,
  },
  {
    id: "account",
    label: "Account",
    description: "Sign-in, GitHub access, active sessions, tokens, and team members.",
    icon: AccountIcon,
  },
  {
    id: "advanced",
    label: "Advanced",
    description: "Switch desktop Stations and inspect installation and gateway status.",
    icon: GatewayIcon,
  },
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
    hint: "When your harness settings or git identity change on this computer, Meridian offers to review them. Nothing uploads until you save.",
    chrome: "fields",
    layout: "split",
    Control: SetupCheckSetting,
  },
  {
    id: "git-identity",
    section: "general",
    label: "Git commit identity",
    keywords: "name email git commit author",
    hint: "Agents commit as this. Set both name and email to enable it. SSH keys are not stored.",
    chrome: "fields",
    layout: "stack",
    Control: GitIdentitySetting,
  },
  {
    id: "capsule-github-access",
    section: "general",
    label: "Give Capsules a GitHub App token",
    keywords: "github app gh git token capsule repository installation access",
    hint: "When a GitHub App is installed for the repository, agents use gh and git with a short-lived token. Takes effect on the next session.",
    chrome: "fields",
    layout: "split",
    Control: CapsuleGitHubAccessSetting,
  },
  {
    id: "desktop-alerts",
    section: "general",
    label: "Desktop alerts",
    keywords: "notifications notify alerts",
    hint: 'Shows a count and "Meridian needs you" when a session waits for input. Always on in the desktop app.',
    chrome: "fields",
    layout: "split",
    Control: DesktopAlertsSetting,
  },
  {
    id: "default-harness",
    section: "general",
    label: "Default harness for a new project",
    keywords: "pack remembered last",
    hint: "Preselected when creating a project. Falls back to the first installed pack.",
    chrome: "fields",
    layout: "split",
    Control: DefaultHarnessSetting,
  },
  {
    id: "provider-connections",
    section: "models",
    label: "Provider connections",
    keywords: "api key sign in endpoint model base url plain http chatgpt claude google vllm ollama litellm",
    hint: "Sign in or save an API key or model endpoint once, then authorize it for each Project.",
    chrome: "fields",
    layout: "wide",
    Control: ProviderConnectionsSetting,
  },
  {
    id: "default-connections",
    section: "models",
    label: "Default connection",
    keywords: "provider profile harness claude codex opencode pi deepseek",
    hint: "Preselected at launch for each harness. Each Project still asks once before using your login.",
    chrome: "fields",
    layout: "wide",
    Control: DefaultConnectionsSetting,
  },
  {
    id: "harness-setups",
    section: "models",
    label: "Harness setups",
    keywords: "default setup import files skills use by default",
    hint: "New Capsules start from the setup marked default. Existing Capsules keep the revision they started with.",
    chrome: "fields",
    layout: "wide",
    Control: HarnessSetupsSetting,
  },
  {
    id: "project-harnesses",
    section: "projects",
    label: "Harness packs",
    keywords: "agents add pack project",
    hint: "Installed packs available on the selected project. Adding one offers it on the next Capsule.",
    chrome: "fields",
    layout: "wide",
    Control: ProjectHarnessesSetting,
  },
  {
    id: "project-checks",
    section: "projects",
    label: "Checks",
    keywords: "command timeout rework attempts automatically send failures",
    hint: "Commands run inside the Capsule when the agent finishes. Failures can be returned to the agent automatically.",
    chrome: "fields",
    layout: "wide",
    Control: ProjectChecksSetting,
  },
  {
    id: "project-environment",
    section: "projects",
    label: "Project environment",
    keywords: "setup script reuse prepared preparation",
    hint: "Preparation runs inside the Capsule before work begins. Saved changes apply to future Capsules.",
    chrome: "fields",
    layout: "wide",
    Control: ProjectEnvironmentSetting,
  },
  {
    id: "notes-delivery",
    section: "projects",
    label: "Deliver notes to",
    keywords: "project notes every thread leads only off deliver to",
    hint: "Copied as files into each new session. Routines opt in individually.",
    chrome: "fields",
    layout: "split",
    Control: NotesDeliverySetting,
  },
  {
    id: "lead-pause",
    section: "agents",
    label: "Pause without you",
    keywords: "unattended lead subagent",
    hint: "Stops starting new subagents after this long without a message from you. Default is 1 day.",
    chrome: "fields",
    layout: "split",
    Control: LeadPauseSetting,
  },
  {
    id: "lead-at-once",
    section: "agents",
    label: "Run at once",
    keywords: "max live capsules subagent",
    hint: "Maximum subagent Capsules the lead can run concurrently. Default is 4, up to 16.",
    chrome: "fields",
    layout: "split",
    Control: LeadAtOnceSetting,
  },
  {
    id: "lead-cleanup",
    section: "agents",
    label: "Clean up finished subagents",
    keywords: "archive idle",
    hint: "Deletes a finished subagent's Capsule after this long idle, unless it has unsaved changes. Default is 1 hour.",
    chrome: "fields",
    layout: "split",
    Control: LeadCleanupSetting,
  },
  {
    id: "lead-approve",
    section: "agents",
    label: "Approve each spawn",
    keywords: "spawn approval operator auto",
    hint: "When off, the lead starts subagents automatically. Turn on to approve every spawn.",
    chrome: "fields",
    layout: "split",
    Control: LeadApproveSetting,
  },
  {
    id: "routines",
    section: "routines",
    label: "Routines",
    keywords: "schedule repeat weekly cadence model effort notes limits prompt name timezone",
    hint: "New routines start weekly on Monday at 06:00 in your time zone, stop after 120 minutes, catch up within 60, and pause after 5 waiting runs.",
    chrome: "panel",
    Control: RoutinesSetting,
  },
  {
    id: "theme",
    section: "appearance",
    label: "Color theme",
    keywords: "light dark system appearance",
    hint: "System follows your operating system appearance. Remembered locally on this device.",
    chrome: "fields",
    layout: "wide",
    Control: ThemeSetting,
  },
  {
    id: "check-updates",
    section: "updates",
    label: "Check for updates",
    keywords: "upgrade restart later desktop version release channel",
    hint: "The desktop app checks on launch, every four hours, after wake, and when shown. Nothing installs until you confirm.",
    chrome: "panel",
    Control: CheckUpdatesSetting,
  },
  {
    id: "account",
    section: "account",
    label: "Account",
    keywords: "signed in email role sign out github sessions token read-only members claim",
    hint: "Sign-in, GitHub, sessions, personal access tokens, and members.",
    chrome: "panel",
    Control: AccountSetting,
  },
  {
    id: "station",
    section: "advanced",
    label: "Station",
    keywords: "gateway switch manage shortcuts",
    hint: "Switch between saved Stations in the desktop app. The first nine use the modifier + 1–9 shortcuts.",
    chrome: "fields",
    layout: "split",
    Control: StationSetting,
  },
  {
    id: "team-mode",
    section: "advanced",
    label: "Installation & gateway",
    keywords: "team mode claim installation provider gateway isolation runtime",
    hint: "Team mode is offered once before claiming and cannot be undone. Reusable provider connections require the provider gateway.",
    chrome: "fields",
    layout: "wide",
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
