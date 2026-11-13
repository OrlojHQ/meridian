import { useState, useRef, type ChangeEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { HarnessSetup, HarnessSetupPreview } from "../api/generated/types.gen";
import { CopyButton } from "./ui/CopyButton";
import { LocalSetupError, directoryPicker, scanSetupDirectory, harnessFolders, readLocalFiles, selectLocalFiles, type Harness, type Exclusion, type Selection } from "../import/localSetup";
import { HarnessMark } from "./ui/HarnessMark";

function importError(cause: unknown) {return cause instanceof LocalSetupError ? cause.message : normalizeAPIError(cause).message;}

const displayNames: Record<Harness, string> = { opencode: "OpenCode", codex: "Codex", claude: "Claude", pi: "Pi", dsh: "DeepSeek Harness" };

// Files outside the configuration folder that only the CLI importer reads,
// per images/capsule-*/manifest.yaml (homeFiles and shared).
const cliExtras: Record<Harness, string> = { opencode: "", codex: "~/.agents/skills", claude: "~/.claude.json", pi: "", dsh: "~/.agents/skills" };

// A page served from this computer talks to a local daemon, so the CLI needs
// no server flag. Otherwise it must be pointed at this server and its token.
function loopbackPage() {
  const host = window.location.hostname;
  return host === "localhost" || host === "127.0.0.1" || host === "[::1]" || host === "::1" || host.endsWith(".localhost");
}
function importCommand(harness: Harness) {
  return loopbackPage() ? `meridian harness import ${harness}` : `meridian --server ${window.location.origin} --token-file TOKEN_FILE harness import ${harness}`;
}
export function HarnessSetupImport({ title = "Import your setup", initialHarness = "opencode", setups, onSaved, onCancel }: {
  title?: string; initialHarness?: Harness; setups: HarnessSetup[]; onSaved?: (setup: HarnessSetup) => void; onCancel?: () => void;
}) {
  const client = useQueryClient();
  const folderInput = useRef<HTMLInputElement>(null);
  const skillsInput = useRef<HTMLInputElement>(null);
  const filesInput = useRef<HTMLInputElement>(null);
  const [harness, setHarness] = useState<Harness>(initialHarness);
  const [selection, setSelection] = useState<Selection[]>([]);
  const [excluded, setExcluded] = useState<Exclusion[]>([]);
  const [target, setTarget] = useState<HarnessSetup>();
  const saveAttempt = useRef<{ fingerprint: string; key: string } | undefined>(undefined);
  const [preview, setPreview] = useState<HarnessSetupPreview>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState<HarnessSetup>();
  const [acceptExclusions, setAcceptExclusions] = useState(false);
  const [name, setName] = useState("");
  const [makeDefault, setMakeDefault] = useState(true);
  const existing = setups.find(setup => setup.harness === harness && setup.default && !setup.deleted);
  const issues = [...excluded, ...(preview?.issues ?? [])];
  function mergeSelection(next: {included: Selection[]; excluded: Exclusion[]}) {
    const merged = new Map(selection.map(item => [item.path, item]));
    for (const item of next.included) {
      if (merged.has(item.path)) throw new LocalSetupError(`${item.path} is already selected. Remove it first to replace it.`);
      merged.set(item.path, item);
    }
    const included = [...merged.values()];
    if (included.length > 256 || included.reduce((sum,item) => sum + item.file.size,0) > 512*1024) throw new LocalSetupError("Select at most 256 files and 512 KiB of configuration.");
    setSelection(included); setExcluded([...excluded,...next.excluded].slice(0,4096)); setPreview(undefined); setSaved(undefined); setError(""); setAcceptExclusions(false);
  }
  function choose(event: ChangeEvent<HTMLInputElement>, prefix = "") {
    try { if (event.currentTarget.files?.length) mergeSelection(selectLocalFiles(harness, Array.from(event.currentTarget.files), prefix)); }
    catch (cause) { setError(importError(cause)); }
    event.currentTarget.value = "";
  }
  async function chooseFolder(skills = false) {
    const picker = directoryPicker();
    if (!picker) { (skills ? skillsInput : folderInput).current?.click(); return; }
    setBusy(true); setError("");
    try { mergeSelection(await scanSetupDirectory(harness, await picker({mode: "read"}), skills ? "skills/" : "")); }
    catch (cause) { if (!(cause instanceof DOMException && cause.name === "AbortError")) setError(importError(cause)); }
    finally { setBusy(false); }
  }
  async function review() {
    setBusy(true); setError("");
    try {
      const files = await readLocalFiles(selection);
      const result = await api.previewHarnessSetup({ harness, files });
      setPreview(result); setTarget(existing); saveAttempt.current=undefined; setSelection([]); setAcceptExclusions(false);
      setName(existing?.name ?? `My ${displayNames[harness]} setup`);
    } catch (cause) { setError(importError(cause)); }
    finally { setBusy(false); }
  }
  async function save() {
    if (!preview) return;
    setBusy(true); setError("");
    try {
      const input = { id: target?.id, name: name.trim(), bundle: preview.bundle, default: makeDefault, expectedResourceVersion: target?.resourceVersion ?? 0 };
      const fingerprint = JSON.stringify(input);
      if (saveAttempt.current?.fingerprint !== fingerprint) saveAttempt.current = {fingerprint, key: crypto.randomUUID()};
      const setup = await api.importHarnessSetup(input, saveAttempt.current.key);
      setPreview(undefined); setExcluded([]); setSaved(setup); saveAttempt.current=undefined;
      await client.invalidateQueries({ queryKey: ["harness-setups"] });
      onSaved?.(setup);
    } catch (cause) { setError(importError(cause)); }
    finally { setBusy(false); }
  }
  return <section className="panel setup-import" id="import-setup">
    <h2>{title}</h2>
    {!onCancel && <label>Harness<span className="select-field with-mark"><HarnessMark harness={harness}/><select value={harness} disabled={busy} onChange={event => {setHarness(event.target.value as Harness);setSelection([]);setExcluded([]);setPreview(undefined);setSaved(undefined);setError("");}}>{Object.entries(displayNames).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></span></label>}
    {saved && !preview && <p role="status" className="setup-import-saved">Saved {saved.name}. New {displayNames[harness]} Capsules start with it. Import again here to add more files or skills.</p>}
    {!preview && <>
      <p>{existing ? <>Import again to update <strong>{existing.name}</strong>. You review every file, and saving creates a new revision; existing Capsules keep theirs.</> : <>Import your {displayNames[harness]} settings, skills, and commands so new Capsules start with them. You review every file before anything is saved.</>}</p>
      {!onCancel && <div className="setup-import-cli">
        <p><strong>Fastest: import from a terminal</strong> {loopbackPage() ? "on this computer" : "on the computer that has these files"}. It reads <code>{harnessFolders[harness]}</code> directly{cliExtras[harness] && <>, plus <code>{cliExtras[harness]}</code></>}, and shows the same review.</p>
        <p className="setup-import-copy"><code>{importCommand(harness)}</code> <CopyButton text={importCommand(harness)} label="Copy command" /></p>
      </div>}
      <ol className="setup-import-steps">
        <li className="setup-import-picker">
          <h3>Choose your {displayNames[harness]} folder</h3>
          <p className="setup-import-copy">Usually <code>{harnessFolders[harness]}</code> <CopyButton text={harnessFolders[harness]} label="Copy path" /></p>
          <p className="setup-import-hint">A web page cannot open a path by itself, so pick the folder in the file picker. On a Mac, press Command-Shift-G in the picker and paste <code>{harnessFolders[harness]}</code>. Elsewhere, show hidden files.</p>
          <input ref={folderInput} hidden aria-label="Choose configuration folder" type="file" multiple {...{ webkitdirectory: "" }} disabled={busy} onChange={event => choose(event)} />
          <input ref={skillsInput} hidden aria-label="Choose skills folder" type="file" multiple {...{ webkitdirectory: "" }} disabled={busy} onChange={event => choose(event, "skills/")} />
          <input ref={filesInput} hidden aria-label="Choose configuration files" type="file" multiple disabled={busy} onChange={event => choose(event)} />
          <div className="setup-import-actions">
            <button className="primary-button" type="button" disabled={busy} onClick={() => void chooseFolder()}>Choose folder</button>
            <button type="button" disabled={busy} onClick={() => filesInput.current?.click()}>Choose individual files</button>
            <button type="button" disabled={busy} onClick={() => void chooseFolder(true)}>Add skills folder</button>
          </div>
          <p className="setup-import-hint">Skills in another folder, such as <code>~/.agents/skills</code>? Add that folder too; its skills are copied into this harness’s skills folder.</p>
          {harness === "claude" && <p className="setup-import-hint">For global MCP settings, also choose <code>~/.claude.json</code> as an individual file.</p>}
          {harness === "dsh" && <p className="setup-import-hint">DeepSeek Harness reads <code>~/.dsh</code> (<code>$DSH_HOME</code>). Credentials in <code>.credentials.yaml</code> are never imported; use a provider connection for models.</p>}
          {!directoryPicker() && <p className="setup-import-hint">This browser’s folder picker counts every file in its confirmation, including excluded dependencies. Meridian filters them afterward; nothing is sent until Review import.</p>}
          {selection.length > 0 && <><p>{selection.length} files selected</p><ul>{selection.map(item => <li key={item.path}>{item.path} <button type="button" disabled={busy} aria-label={`Remove ${item.path}`} onClick={() => setSelection(current => current.filter(value => value.path !== item.path))}>Remove</button></li>)}</ul></>}
          {!selection.length && excluded.length > 0 && <p>No supported files selected. Choose the {harnessFolders[harness]} folder.</p>}
        </li>
        <li className="setup-import-picker">
          <h3>Review and save</h3>
          <p className="setup-import-hint">Review import uploads the selected files to your Meridian server for checks. Dependency folders such as node_modules and login caches are excluded; you sign in to each harness separately.</p>
          <button type="button" disabled={busy || !selection.length} onClick={() => void review()}>{busy ? "Reviewing…" : "Review import"}</button>
          {!selection.length && <span className="setup-import-hint"> Choose a folder or files first.</span>}
        </li>
      </ol>
    </>}
    {preview && <>
      <h3>Review what will transfer</h3>
      <p>{preview.bundle.files.length} portable files. {target ? "Saving creates a new revision of your existing default setup. Existing Capsules keep their pinned revision." : "Saving makes this setup available to future Capsules."}</p>
      <label>Setup name<input value={name} maxLength={128} disabled={busy} onChange={event => setName(event.target.value)} /></label>
      <label><input type="checkbox" checked={makeDefault} disabled={busy} onChange={event => setMakeDefault(event.target.checked)} />Use by default for {displayNames[harness]}</label>
      {preview.bundle.files.map((file, index) => <details key={file.path}><summary>{file.path}</summary><pre className="setup-import-content">{file.content}</pre><label><input type="checkbox" checked={file.executable ?? false} disabled={busy} onChange={event => {const executable = event.target.checked;setPreview(current => current ? {...current,bundle:{...current.bundle,files:current.bundle.files.map((item,i) => i === index ? {...item,executable} : item)}} : current);}} />Allow this file to run as an executable inside the Capsule</label></details>)}
      {!!preview.bundle.dependencies?.length && <><h3>Tools to install inside the Capsule</h3><ul>{preview.bundle.dependencies.map(pkg => <li key={pkg}>{pkg}</li>)}</ul></>}
    </>}
    {!!preview?.warnings?.length && <details open><summary>{preview.warnings.length} portability warnings — settings kept</summary><ul>{preview.warnings.map((warning,index) => <li key={index}><code>{warning.path}</code>: {warning.reason}</li>)}</ul></details>}
    {issues.length > 0 && <details open={Boolean(preview)}><summary>{issues.length} exclusions</summary><ul>{issues.map((issue,index) => <li key={index}><code>{issue.path}</code>: {issue.reason}</li>)}</ul></details>}
    {preview && <>
      {!!issues.length && <label><input type="checkbox" checked={acceptExclusions} disabled={busy} onChange={event => setAcceptExclusions(event.target.checked)} />I reviewed the exclusions</label>}
      <button type="button" disabled={busy || !name.trim() || !preview.bundle.files.length || (!!issues.length && !acceptExclusions)} onClick={() => void save()}>{busy ? "Saving…" : "Save setup"}</button>{" "}
      <button type="button" disabled={busy} onClick={() => {setPreview(undefined);setExcluded([]);setError("");}}>Choose different files</button>
    </>}
    {error && <p role="alert">{error}</p>}
    {onCancel && <button type="button" disabled={busy} onClick={onCancel}>Back to Capsule</button>}
  </section>;
}
