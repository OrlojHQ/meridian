// Design mock for a single Settings window. Development only, and only when
// the page is opened with ?design=settings. This is not the Settings feature:
// nothing here is saved, and the controls are sample values.

import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

import { HarnessMark } from "../components/ui/HarnessMark";
import {
  AccountIcon,
  CloseIcon,
  FilesIcon,
  GatewayIcon,
  RoutineIcon,
  SearchIcon,
  SettingsIcon,
  ThemeIcon,
  ThreadIcon,
  ToolIcon,
  UpdateIcon,
} from "../components/ui/Icons";
import { ProviderMark } from "../components/ui/ProviderMark";
import { Button } from "../components/ui/Button";
import { Input, Select, TextArea } from "../components/ui/Field";
import { rememberTheme, themeLabel, type ThemePreference } from "../components/ui/theme";
import { isApplePlatform } from "../shell/paletteCommands";
import "./settings-mock.css";

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

const sections: { id: SettingsSectionId; label: string; icon: typeof SettingsIcon }[] = [
  { id: "general", label: "General", icon: SettingsIcon },
  { id: "models", label: "Models & Harnesses", icon: ToolIcon },
  { id: "projects", label: "Projects", icon: FilesIcon },
  { id: "agents", label: "Agents", icon: ThreadIcon },
  { id: "routines", label: "Routines", icon: RoutineIcon },
  { id: "appearance", label: "Appearance", icon: ThemeIcon },
  { id: "updates", label: "Updates", icon: UpdateIcon },
  { id: "account", label: "Account", icon: AccountIcon },
  { id: "advanced", label: "Advanced", icon: GatewayIcon },
];

const harnesses = ["Claude Code", "Codex", "OpenCode", "Pi", "DeepSeek Harness"] as const;
const connectionNames = ["No default connection", "Claude", "OpenAI API"] as const;
const deliveryOptions = ["Every Thread", "Lead Threads and their subagents", "No Thread"] as const;
const pauseOptions = ["1 hour", "4 hours", "1 day", "3 days"] as const;
const cleanupOptions = ["15 minutes", "1 hour", "4 hours", "1 day", "Never"] as const;
const repeatOptions = ["Every hour", "Every day", "Weekdays", "Every week", "Custom cron"] as const;
const weekdays = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"] as const;

function shortcutKeys() {
  return isApplePlatform() ? ["⌘", ","] : ["Ctrl", ","];
}

function Shortcut() {
  return (
    <span className="key-combo" aria-hidden="true">
      {shortcutKeys().map((key) => (
        <kbd key={key}>{key}</kbd>
      ))}
    </span>
  );
}

function matches(query: string, text: string) {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  return text.toLowerCase().includes(needle);
}

function Row({
  id,
  label,
  hint,
  keywords = "",
  query,
  children,
}: {
  id: string;
  label: string;
  hint?: string;
  keywords?: string;
  query: string;
  children: ReactNode;
}) {
  if (!matches(query, `${label} ${hint ?? ""} ${keywords}`)) return null;
  return (
    <div className="settings-mock-row">
      <div>
        <label className="settings-mock-label" htmlFor={id}>
          {label}
        </label>
        {hint ? <p>{hint}</p> : null}
      </div>
      <div className="settings-mock-control">{children}</div>
    </div>
  );
}

function Heading({ title, query, text }: { title: string; query: string; text: string }) {
  if (query.trim() && !matches(query, `${title} ${text}`)) return null;
  return <h2>{title}</h2>;
}

function Group({
  title,
  query,
  text,
  children,
}: {
  title: string;
  query: string;
  text: string;
  children: ReactNode;
}) {
  if (!matches(query, `${title} ${text}`)) return null;
  return (
    <div className="settings-mock-block">
      <h2>{title}</h2>
      {children}
    </div>
  );
}

function Scope({
  id,
  label,
  value,
  options,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  options: string[];
  onChange: (value: string) => void;
}) {
  return (
    <div className="settings-mock-scope">
      <span>{label}</span>
      <Select id={id} aria-label={label} value={value} onChange={(event) => onChange(event.target.value)}>
        {options.map((option) => (
          <option key={option} value={option}>
            {option}
          </option>
        ))}
      </Select>
    </div>
  );
}

function General({ query }: { query: string }) {
  const [setupCheck, setSetupCheck] = useState(true);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [harness, setHarness] = useState("Claude Code");
  const dirty = name.trim() !== "" || email.trim() !== "";
  const pair = (name.trim() === "") !== (email.trim() === "");
  return (
    <>
      <Heading title="This computer" query={query} text="setup check git name email identity save alerts notifications desktop" />
      <Row
        id="setup-check"
        query={query}
        label="Check this computer for setup changes"
        hint="When your harness settings or git identity change, Meridian offers to review them. Nothing is uploaded until you click Save. On in the desktop app. Hidden in a browser."
        keywords="desktop import laptop"
      >
        <input
          id="setup-check"
          type="checkbox"
          checked={setupCheck}
          onChange={(event) => setSetupCheck(event.target.checked)}
        />
      </Row>
      <Row
        id="git-name"
        query={query}
        label="Name"
        hint="Git commit identity. Agents inside a Capsule commit as this, and Delivery uses it when a Project has no commit author. Empty until you set it."
        keywords="git commit identity"
      >
        <Input id="git-name" value={name} maxLength={320} placeholder="Name" onChange={(event) => setName(event.target.value)} />
      </Row>
      <Row
        id="git-email"
        query={query}
        label="Email"
        hint="Kept with the name. SSH keys and the rest of your git config are not."
        keywords="git commit identity"
      >
        <Input
          id="git-email"
          type="email"
          value={email}
          maxLength={320}
          placeholder="Email"
          onChange={(event) => setEmail(event.target.value)}
        />
      </Row>
      <Row id="git-save" query={query} label="Save identity" keywords="git commit" hint="Both fields stay empty, or both are filled in.">
        <Button id="git-save" variant="secondary" disabled={!dirty || pair}>
          Save identity
        </Button>
      </Row>
      <Row
        id="alerts"
        query={query}
        label="Desktop alerts"
        hint={'A count and "Meridian needs you". Nothing is sent to a phone. The desktop app always shows this as on. A browser shows Notify me until permission is granted, or Alerts blocked if permission was denied.'}
        keywords="notifications notify"
      >
        <span id="alerts">Desktop alerts on</span>
      </Row>
      <Heading title="New projects" query={query} text="default harness for a new project pack" />
      <Row
        id="default-harness"
        query={query}
        label="Default harness for a new project"
        hint="Remembers the last pack you chose. With nothing remembered, the first installed pack is selected."
        keywords="meridian.newProjectHarness"
      >
        <Select id="default-harness" value={harness} onChange={(event) => setHarness(event.target.value)}>
          {harnesses.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </Select>
      </Row>
    </>
  );
}

function Models({ query }: { query: string }) {
  const [connections, setConnections] = useState<Record<string, string>>({
    "Claude Code": "Claude",
    Codex: "No default connection",
    OpenCode: "No default connection",
    Pi: "No default connection",
    "DeepSeek Harness": "No default connection",
  });
  const [setup, setSetup] = useState("Daily");
  const [provider, setProvider] = useState("OpenAI");
  const [connectionName, setConnectionName] = useState("");
  const custom = provider.endsWith("endpoint");
  return (
    <>
      <Heading title="Default connection" query={query} text="default connection for harness provider profile setup" />
      {harnesses.map((harness) => (
        <Row
          key={harness}
          id={`conn-${harness}`}
          query={query}
          label={`Default connection for ${harness}`}
          hint={
            harness === "Claude Code"
              ? "Preselected at launch. Each Project still asks once before it uses your login. Default is no connection."
              : undefined
          }
          keywords="provider profile"
        >
          <Select
            id={`conn-${harness}`}
            value={connections[harness]}
            onChange={(event) => setConnections((current) => ({ ...current, [harness]: event.target.value }))}
          >
            {connectionNames.map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </Select>
        </Row>
      ))}
      <Row
        id="default-setup"
        query={query}
        label="Default setup for Claude Code"
        hint="New Capsules of this harness start with the setup marked default. Existing Capsules keep the revision they started with."
        keywords="use by default import files skills"
      >
        <Select id="default-setup" value={setup} onChange={(event) => setSetup(event.target.value)}>
          <option value="none">No setup imported</option>
          <option value="Daily">Daily</option>
        </Select>
      </Row>
      <Group
        title="Provider connections"
        query={query}
        text="Provider connections Claude Signed in OpenAI API Saved Add a connection API key Base URL Default model plain HTTP ChatGPT"
      >
        <ul className="connection-list" aria-label="Saved connections">
          <li className="connection-row">
            <ProviderMark provider="anthropic" />
            <div className="connection-main">
              <div className="connection-title">
                <strong>Claude</strong>
                <span className="connection-status ok">Signed in</span>
              </div>
              <p className="connection-meta">Claude account</p>
            </div>
          </li>
          <li className="connection-row">
            <ProviderMark provider="openai" />
            <div className="connection-main">
              <div className="connection-title">
                <strong>OpenAI API</strong>
                <span className="connection-status ok">Saved</span>
              </div>
              <p className="connection-meta">OpenAI API key</p>
            </div>
          </li>
        </ul>
        <div className="settings-mock-stack">
          <label htmlFor="provider">
            Provider
            <Select id="provider" value={provider} onChange={(event) => setProvider(event.target.value)}>
              <option>OpenAI</option>
              <option>Claude</option>
              <option>ChatGPT plan</option>
              <option>OpenAI-compatible endpoint</option>
              <option>Anthropic-compatible endpoint</option>
            </Select>
          </label>
          {provider !== "ChatGPT plan" && (
            <label htmlFor="connection-name">
              Connection name
              <Input id="connection-name" value={connectionName} maxLength={128} onChange={(event) => setConnectionName(event.target.value)} />
            </label>
          )}
          {custom && (
            <>
              <label htmlFor="base-url">
                Base URL
                <Input id="base-url" placeholder="https://models.example.com" />
              </label>
              <label htmlFor="default-model">
                Default model
                <Input id="default-model" placeholder="model-id" />
              </label>
              <label className="checkbox-label" htmlFor="private-http">
                <input id="private-http" type="checkbox" />
                Use plain HTTP to a private-network server
              </label>
            </>
          )}
          {provider === "Claude" && (
            <p className="settings-mock-note">Sign in with your Claude account, or use an API key or a setup token instead.</p>
          )}
          {provider === "ChatGPT plan" && (
            <p className="settings-mock-note">Sign in once so Codex uses your plan. The login stays encrypted on this installation.</p>
          )}
          {provider === "OpenAI" && (
            <label htmlFor="api-key">
              API key
              <Input id="api-key" type="password" autoComplete="off" />
            </label>
          )}
          {provider !== "Claude" && provider !== "ChatGPT plan" && (
            <div>
              <Button variant="primary">Save connection</Button>
            </div>
          )}
        </div>
      </Group>
      <Group title="Import" query={query} text="Review this computer's setup import folder meridian import">
        <p>Imports come from the desktop import window, meridian import, or a folder picker. Credentials are left out.</p>
        <div className="settings-mock-inline-actions">
          <Button variant="primary">Review this computer's setup</Button>
        </div>
      </Group>
    </>
  );
}

function Projects({ query }: { query: string }) {
  const [project, setProject] = useState("meridian");
  const [rework, setRework] = useState(true);
  const [attempts, setAttempts] = useState("3");
  const [reuse, setReuse] = useState(true);
  const [delivery, setDelivery] = useState<(typeof deliveryOptions)[number]>("Every Thread");
  const [script, setScript] = useState("");
  return (
    <>
      <Scope id="project-scope" label="Project" value={project} options={["meridian", "billing-api"]} onChange={setProject} />
      <Group
        title="Harness packs"
        query={query}
        text="Harness packs Claude Code Codex OpenCode Pi DeepSeek Harness add agent"
      >
        <p>Packs on {project}. Adding one offers it for the next Capsule. Sample shows the packs on meridian.</p>
        <div className="settings-mock-packs">
          {harnesses.map((harness) => (
            <span key={harness} className="settings-mock-pack">
              <HarnessMark harness={harness === "Claude Code" ? "claude" : harness === "Codex" ? "codex" : harness === "OpenCode" ? "opencode" : harness === "Pi" ? "pi" : "dsh"} size="sm" />
              {harness}
            </span>
          ))}
        </div>
      </Group>
      <Heading title="Checks" query={query} text="automatically send failures attempts rework command timeout" />
      <Row
        id="auto-rework"
        query={query}
        label="Automatically send failures back to the agent"
        hint="When a check fails, Meridian sends the failure back and tries again, up to the attempt limit. On by default."
        keywords="auto rework"
      >
        <input id="auto-rework" type="checkbox" checked={rework} onChange={(event) => setRework(event.target.checked)} />
      </Row>
      <Row
        id="attempts"
        query={query}
        label="Attempts"
        hint="3 runs the checks up to three times. 0 runs them once and stops, even if they fail."
        keywords="max rework"
      >
        <Input id="attempts" inputMode="numeric" value={attempts} onChange={(event) => setAttempts(event.target.value)} />
      </Row>
      <Group title="Check commands" query={query} text="Check name command timeout seconds make test 600">
        <p>Run inside the Capsule when the agent finishes. A new Project starts with no commands. The editor offers one blank row named test, timeout 600 seconds.</p>
        <div className="settings-mock-checks">
          <label>
            Name
            <Input aria-label="Check 1 name" defaultValue="test" maxLength={64} />
          </label>
          <label>
            Command
            <Input aria-label="Check 1 command" placeholder="make test" />
          </label>
          <label>
            Timeout (seconds)
            <Input aria-label="Check 1 timeout" inputMode="numeric" defaultValue="600" />
          </label>
        </div>
        <div className="settings-mock-inline-actions">
          <Button>Add a check</Button>
          <Button variant="primary">Save checks</Button>
        </div>
      </Group>
      <Heading title="Environment" query={query} text="reuse matching prepared environments setup script" />
      <Row
        id="reuse-env"
        query={query}
        label="Reuse matching prepared environments"
        hint="On by default. Saved changes apply to future Capsules."
        keywords="preparation setup"
      >
        <input id="reuse-env" type="checkbox" checked={reuse} onChange={(event) => setReuse(event.target.checked)} />
      </Row>
      <Group title="Setup script" query={query} text="Setup script npm ci environment preparation">
        <div className="settings-mock-stack">
          <label htmlFor="setup-script">
            Setup script
            <TextArea id="setup-script" rows={4} maxLength={4096} placeholder="npm ci" value={script} onChange={(event) => setScript(event.target.value)} />
          </label>
          <div className="settings-mock-inline-actions">
            <Button variant="primary">Save environment</Button>
            <Button>Rebuild on next launch</Button>
          </div>
        </div>
      </Group>
      <Heading title="Notes" query={query} text="deliver to project notes every thread default" />
      <Row
        id="notes-delivery"
        query={query}
        label="Deliver to"
        hint="Copied as files into each new session. Routines opt in one by one. Default is Every Thread."
        keywords="project notes all threads leads only off"
      >
        <Select id="notes-delivery" value={delivery} onChange={(event) => setDelivery(event.target.value as typeof delivery)}>
          {deliveryOptions.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </Select>
      </Row>
    </>
  );
}

function Agents({ query }: { query: string }) {
  const [lead, setLead] = useState("docs overhaul");
  const [pause, setPause] = useState("1 day");
  const [atOnce, setAtOnce] = useState("4");
  const [cleanup, setCleanup] = useState("1 hour");
  const [approve, setApprove] = useState(false);
  return (
    <>
      <Scope id="lead-scope" label="Lead" value={lead} options={["docs overhaul"]} onChange={setLead} />
      <p className="settings-mock-note">
        These belong to the lead Thread, not the installation. A new lead starts with the defaults below. Starting a
        Capsule still asks Thread or CLI, Lead agent with subagents (off), and Approve each subagent spawn (off).
        Include Project notes follows Deliver to.
      </p>
      <Row
        id="pause-without-you"
        query={query}
        label="Pause without you"
        hint="The lead stops starting subagents after this long without a message from you. Default is 1 day."
        keywords="unattended"
      >
        <Select id="pause-without-you" value={pause} onChange={(event) => setPause(event.target.value)}>
          {pauseOptions.map((option) => (
            <option key={option}>{option}</option>
          ))}
        </Select>
      </Row>
      <Row
        id="run-at-once"
        query={query}
        label="Run at once"
        hint="Subagents the lead can run at a time. Default is 4, and the most is 16."
        keywords="max live capsules"
      >
        <Select id="run-at-once" value={atOnce} onChange={(event) => setAtOnce(event.target.value)}>
          {Array.from({ length: 16 }, (_, index) => String(index + 1)).map((option) => (
            <option key={option}>{option}</option>
          ))}
        </Select>
      </Row>
      <Row
        id="cleanup"
        query={query}
        label="Clean up finished subagents"
        hint="Deletes a done subagent's Capsule after this long idle, unless it has unsaved changes. Default is 1 hour. Never leaves them."
        keywords="archive"
      >
        <Select id="cleanup" value={cleanup} onChange={(event) => setCleanup(event.target.value)}>
          {cleanupOptions.map((option) => (
            <option key={option}>{option}</option>
          ))}
        </Select>
      </Row>
      <Row
        id="approve-spawn"
        query={query}
        label="Approve each spawn"
        hint="When off, the lead starts subagents on its own. Default is off."
        keywords="spawn approval operator"
      >
        <input id="approve-spawn" type="checkbox" checked={approve} onChange={(event) => setApprove(event.target.checked)} />
      </Row>
    </>
  );
}

function Routines({ query }: { query: string }) {
  const [project, setProject] = useState("meridian");
  const [enabled, setEnabled] = useState(true);
  const [repeat, setRepeat] = useState("Every week");
  const [day, setDay] = useState("Monday");
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [notes, setNotes] = useState(false);
  return (
    <>
      <Scope id="routine-project" label="Project" value={project} options={["meridian", "billing-api"]} onChange={setProject} />
      <p className="settings-mock-note">
        Starts a fresh Capsule on a schedule and never ships. On and off is a button on the routine today.
      </p>
      <Row
        id="routine-enabled"
        query={query}
        label="deps-weekly"
        hint="On. Off skips new runs and keeps the Capsules already waiting."
        keywords="routine enabled turn on turn off"
      >
        <input
          id="routine-enabled"
          type="checkbox"
          role="switch"
          checked={enabled}
          onChange={(event) => setEnabled(event.target.checked)}
        />
      </Row>
      <Row id="routine-repeat" query={query} label="Repeat" hint="Default for a new routine is every week." keywords="cadence cron schedule">
        <Select id="routine-repeat" value={repeat} onChange={(event) => setRepeat(event.target.value)}>
          {repeatOptions.map((option) => (
            <option key={option}>{option}</option>
          ))}
        </Select>
      </Row>
      <Row id="routine-day" query={query} label="Day" hint="Monday is the default weekday." keywords="weekly">
        <Select id="routine-day" value={day} onChange={(event) => setDay(event.target.value)}>
          {weekdays.map((option) => (
            <option key={option}>{option}</option>
          ))}
        </Select>
      </Row>
      <Row id="routine-time" query={query} label="Time" hint="Default is 06:00 in the routine's time zone." keywords="clock">
        <Input id="routine-time" type="time" defaultValue="06:00" />
      </Row>
      <Row
        id="routine-zone"
        query={query}
        label="Time zone"
        hint="A new routine uses this computer's time zone."
        keywords="timezone"
      >
        <Input id="routine-zone" defaultValue="America/Los_Angeles" />
      </Row>
      <Row
        id="routine-harness"
        query={query}
        label="Harness"
        hint="The routine runs this pack."
        keywords="claude code"
      >
        <Select id="routine-harness" defaultValue="Claude Code">
          {harnesses.map((option) => (
            <option key={option}>{option}</option>
          ))}
        </Select>
      </Row>
      <Row
        id="routine-model"
        query={query}
        label="Model"
        hint="Empty uses the harness default."
        keywords="default model"
      >
        <Select id="routine-model" value={model} onChange={(event) => setModel(event.target.value)}>
          <option value="">Harness default</option>
          <option value="sonnet">Sonnet</option>
        </Select>
      </Row>
      <Row id="routine-effort" query={query} label="Effort" hint="Leave empty for the model's default." keywords="reasoning">
        <Select id="routine-effort" value={effort} onChange={(event) => setEffort(event.target.value)}>
          <option value="">Model default</option>
          <option value="low">low</option>
          <option value="high">high</option>
        </Select>
      </Row>
      <Row
        id="routine-notes"
        query={query}
        label="Include Project notes"
        hint="Each run gets the Project's notes as files. A run has no one watching, so this stays off unless you turn it on."
        keywords="notes"
      >
        <input id="routine-notes" type="checkbox" role="switch" checked={notes} onChange={(event) => setNotes(event.target.checked)} />
      </Row>
      <Row id="routine-stop" query={query} label="Stop a run after (minutes)" hint="Default is 120 minutes." keywords="limit">
        <Input id="routine-stop" inputMode="numeric" defaultValue="120" />
      </Row>
      <Row
        id="routine-catchup"
        query={query}
        label="Catch up a missed run within (minutes)"
        hint="Default is 60 minutes."
        keywords="limit"
      >
        <Input id="routine-catchup" inputMode="numeric" defaultValue="60" />
      </Row>
      <Row
        id="routine-kept"
        query={query}
        label="Pause firing while this many runs wait for you"
        hint="Default is 5."
        keywords="max kept"
      >
        <Input id="routine-kept" inputMode="numeric" defaultValue="5" />
      </Row>
    </>
  );
}

function Appearance({ query }: { query: string }) {
  const [theme, setTheme] = useState<ThemePreference>(() => {
    const stored = document.documentElement.dataset.theme;
    if (stored === "light" || stored === "dark") return stored;
    return "system";
  });
  return (
    <>
      <Heading title="Color" query={query} text="color theme light dark system appearance" />
      <Row
        id="theme"
        query={query}
        label="Color theme"
        hint="System follows this computer. The choice is remembered on this browser. The sidebar footer still cycles System, Light, and Dark."
        keywords="light dark appearance"
      >
        <div className="segmented-control" role="radiogroup" aria-label="Color theme">
          {(["system", "light", "dark"] as const).map((value) => (
            <button
              key={value}
              type="button"
              role="radio"
              aria-checked={theme === value}
              className={theme === value ? "active" : undefined}
              onClick={() => {
                setTheme(value);
                rememberTheme(value);
              }}
            >
              {themeLabel(value)}
            </button>
          ))}
        </div>
      </Row>
      <p className="settings-mock-note">Nothing else in the app is a color or density setting. Theme is the whole of Appearance.</p>
    </>
  );
}

function Updates({ query }: { query: string }) {
  return (
    <>
      <Heading title="This installation" query={query} text="check for updates upgrade restart later" />
      <Row
        id="check-updates"
        query={query}
        label="Check for updates"
        hint="The desktop app checks on launch, every four hours, after the computer wakes, and when the window is shown again. Nothing installs until you click Update and restart. Remind me later hides that version for 24 hours. A browser has no updater."
        keywords="upgrade later restart"
      >
        <Button id="check-updates" variant="primary">
          Check for updates
        </Button>
      </Row>
      <p className="settings-mock-note">Meridian is up to date. There is no switch to turn the check off.</p>
    </>
  );
}

function Account({ query }: { query: string }) {
  const [readOnly, setReadOnly] = useState(false);
  const [expiry, setExpiry] = useState("90");
  return (
    <>
      <Heading title="Signed in" query={query} text="signed in as email role sign out account" />
      <Row id="signed-in" query={query} label="Signed in as" hint="Team mode. Single-user mode has no profile, only the installation token." keywords="account ada">
        <span id="signed-in">Ada Lovelace</span>
      </Row>
      <Row id="account-email" query={query} label="Email" keywords="account">
        <span id="account-email">ada@example.com</span>
      </Row>
      <Row id="account-role" query={query} label="Role" keywords="admin member">
        <span id="account-role">admin</span>
      </Row>
      <Row id="sign-out" query={query} label="Sign out" keywords="session">
        <Button id="sign-out" variant="secondary">
          Sign out
        </Button>
      </Row>
      <Heading title="GitHub" query={query} text="github connect reconnect" />
      <Row
        id="github"
        query={query}
        label="GitHub"
        hint="Used to read pull-request checks and open pull requests. Connect and reconnect live here today, at Account."
        keywords="connect reconnect"
      >
        <Button id="github">Reconnect</Button>
      </Row>
      <Heading title="Sessions" query={query} text="this browser session revoke" />
      <Row
        id="sessions"
        query={query}
        label="This browser"
        hint="Sessions end after 12 hours idle or 7 days. Other sessions can be revoked. This one cannot."
        keywords="revoke"
      >
        <span id="sessions">Current</span>
      </Row>
      <Heading title="Personal access tokens" query={query} text="token name expires read-only create" />
      <Row id="token-name" query={query} label="Token name" hint="Tokens let the CLI and TUI act as you. A new token is shown once." keywords="pat">
        <Input id="token-name" maxLength={64} />
      </Row>
      <Row id="token-expiry" query={query} label="Expires after" hint="Default is 90 days." keywords="token">
        <Select id="token-expiry" value={expiry} onChange={(event) => setExpiry(event.target.value)}>
          <option value="30">30 days</option>
          <option value="90">90 days</option>
          <option value="365">1 year</option>
        </Select>
      </Row>
      <Row id="token-readonly" query={query} label="Read-only" hint="Off unless you check it. A read-only token cannot change the installation." keywords="token">
        <input id="token-readonly" type="checkbox" checked={readOnly} onChange={(event) => setReadOnly(event.target.checked)} />
      </Row>
      <Row id="token-create" query={query} label="Create token" keywords="mint">
        <Button id="token-create" variant="primary">
          Create token
        </Button>
      </Row>
      <Group title="Members" query={query} text="Members role admin disable Grace Hopper">
        <p>Admins can change a role or disable a member. Disabling signs them out and stops their Capsules' model traffic. The last enabled admin cannot be demoted or disabled.</p>
      </Group>
    </>
  );
}

function Advanced({ query }: { query: string }) {
  return (
    <>
      <Heading title="Station" query={query} text="station switch manage shortcuts keyboard" />
      <Row
        id="station"
        query={query}
        label="Station"
        hint="Opens the desktop list of saved Stations. The page never receives that list. In a browser this row is absent and the sidebar shows Meridian instead of a Station menu."
        keywords="gateway switch manage"
      >
        <Button id="station">Switch or manage Stations…</Button>
      </Row>
      <Row
        id="station-keys"
        query={query}
        label="Station shortcuts"
        hint="The first nine saved Stations. Desktop only."
        keywords="keyboard"
      >
        <span id="station-keys">{isApplePlatform() ? "⌘1–⌘9" : "Ctrl+1–Ctrl+9"}</span>
      </Row>
      <Heading title="Installation" query={query} text="provider gateway team mode claim" />
      <Row
        id="gateway"
        query={query}
        label="Provider gateway"
        hint="Reusable provider connections need it. When it is off, the connections section says so. There is no switch here: an administrator sets it on the daemon."
        keywords="provider gateway listen"
      >
        <span id="gateway">On</span>
      </Row>
      <Row
        id="team-mode"
        query={query}
        label="Team mode"
        hint="Offered once, before the installation is claimed, and it cannot be undone. This sample is already in team mode, so the claim form is not shown."
        keywords="claim installation token"
      >
        <span id="team-mode">On</span>
      </Row>
    </>
  );
}

const panes: Record<SettingsSectionId, (query: string) => ReactNode> = {
  general: (query) => <General query={query} />,
  models: (query) => <Models query={query} />,
  projects: (query) => <Projects query={query} />,
  agents: (query) => <Agents query={query} />,
  routines: (query) => <Routines query={query} />,
  appearance: (query) => <Appearance query={query} />,
  updates: (query) => <Updates query={query} />,
  account: (query) => <Account query={query} />,
  advanced: (query) => <Advanced query={query} />,
};

// Words that should keep a section in the list while a search is active.
const sectionText: Record<SettingsSectionId, string> = {
  general: "setup check git name email identity save alerts notifications desktop default harness new project",
  models: "default connection setup provider connections claude openai api key base url default model import plain http chatgpt codex",
  projects: "harness packs checks rework attempts command timeout environment reuse setup script notes deliver every thread default",
  agents: "pause without you run at once cleanup approve spawn lead subagent unattended",
  routines: "deps-weekly repeat day time zone harness model effort notes stop catch kept default schedule weekly",
  appearance: "color theme light dark system appearance",
  updates: "check for updates upgrade restart later",
  account: "signed in email role sign out github sessions token read-only members",
  advanced: "station shortcuts provider gateway team mode installation",
};

export function SettingsDesign({
  open,
  section,
  query,
  onOpenChange,
  onSection,
  onQuery,
}: {
  open: boolean;
  section: SettingsSectionId;
  query: string;
  onOpenChange: (open: boolean) => void;
  onSection: (section: SettingsSectionId) => void;
  onQuery: (query: string) => void;
}) {
  const [footer, setFooter] = useState<Element | null>(null);
  const [mounts, setMounts] = useState(0);
  const pending = useRef(false);

  useEffect(() => {
    const sync = () => {
      const node = document.querySelector(".sidebar-footer");
      if (!node) return;
      setFooter((current) => (current === node ? current : node));
      const gear = node.querySelector("[data-settings-gear]");
      if (gear) {
        if (node.firstElementChild !== gear) node.prepend(gear);
        pending.current = false;
        return;
      }
      if (pending.current) return;
      pending.current = true;
      setMounts((value) => value + 1);
    };
    sync();
    const observer = new MutationObserver(sync);
    observer.observe(document.body, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [pending]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape" && open) {
        onOpenChange(false);
        return;
      }
      if (event.key !== "," || event.altKey || event.shiftKey || !(event.metaKey || event.ctrlKey)) return;
      const target = event.target;
      if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) {
        return;
      }
      event.preventDefault();
      onOpenChange(!open);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onOpenChange]);

  useEffect(() => {
    let frames = 0;
    let raf = 0;
    const tick = () => {
      const gear = document.querySelector(".sidebar-footer > [data-settings-gear]");
      const dialogOk = !open || document.querySelector("[data-settings-mock]");
      if (gear && dialogOk) frames += 1;
      else frames = 0;
      if (frames >= 3) {
        document.documentElement.dataset.shotReady = "1";
        return;
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [open, section, query, mounts, footer]);

  const visible = sections.filter((item) => query.trim() === "" || matches(query, `${item.label} ${sectionText[item.id]}`));
  const showing = visible.some((item) => item.id === section) ? section : visible[0]?.id;

  const gear = (
    <button
      type="button"
      className="palette-trigger settings-mock-gear"
      data-settings-gear=""
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-keyshortcuts={isApplePlatform() ? "Meta+Comma" : "Control+Comma"}
      onClick={() => onOpenChange(true)}
    >
      <SettingsIcon />
      Settings
      <Shortcut />
    </button>
  );

  return (
    <>
      {footer ? createPortal(gear, footer) : null}
      {open && (
        <div
          className="dialog-backdrop"
          data-settings-mock=""
          onMouseDown={() => onOpenChange(false)}
        >
          <section
            className="settings-mock"
            role="dialog"
            aria-modal="true"
            aria-labelledby="settings-mock-title"
            onMouseDown={(event) => event.stopPropagation()}
          >
            <header className="settings-mock-top">
              <h1 id="settings-mock-title">Settings</h1>
              <label className="activity-filter settings-mock-search">
                <SearchIcon />
                <span className="sr-only">Search settings</span>
                <input
                  value={query}
                  placeholder="Search settings"
                  onChange={(event) => onQuery(event.target.value)}
                />
              </label>
              <button type="button" className="icon-button" aria-label="Close settings" onClick={() => onOpenChange(false)}>
                <CloseIcon />
              </button>
            </header>
            <div className="settings-mock-body">
              <nav className="settings-mock-nav" aria-label="Settings sections">
                {visible.map((item) => {
                  const Icon = item.icon;
                  const current = !query.trim() && item.id === section;
                  return (
                    <button
                      key={item.id}
                      type="button"
                      aria-current={current ? "page" : undefined}
                      onClick={() => {
                        onQuery("");
                        onSection(item.id);
                      }}
                    >
                      <Icon />
                      {item.label}
                    </button>
                  );
                })}
              </nav>
              <div className="settings-mock-content">
                {visible.length === 0 ? (
                  <p className="settings-mock-empty">No settings match “{query.trim()}”.</p>
                ) : query.trim() ? (
                  visible.map((item) => (
                    <div key={item.id} data-settings-section={item.id}>
                      <p className="settings-mock-hit">{item.label}</p>
                      {panes[item.id](query)}
                    </div>
                  ))
                ) : (
                  panes[showing ?? section]("")
                )}
              </div>
            </div>
            <p className="settings-mock-save">Switches and menus save immediately. A form that collects several fields keeps its Save button.</p>
          </section>
        </div>
      )}
    </>
  );
}
