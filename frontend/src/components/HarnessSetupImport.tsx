import { useState, useRef, type ChangeEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { HarnessSetup, HarnessSetupPreview } from "../api/generated/types.gen";
import { LocalSetupError, directoryPicker, scanSetupDirectory, harnessFolders, readLocalFiles, selectLocalFiles, type Harness, type Exclusion, type Selection } from "../import/localSetup";

function importError(cause: unknown) {return cause instanceof LocalSetupError ? cause.message : normalizeAPIError(cause).message;}

const displayNames: Record<Harness, string> = { opencode: "OpenCode", codex: "Codex", claude: "Claude", pi: "Pi" };
export function HarnessSetupImport({ initialHarness = "opencode", setups, onSaved, onCancel }: {
  initialHarness?: Harness; setups: HarnessSetup[]; onSaved?: (setup: HarnessSetup) => void; onCancel?: () => void;
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
  return <section className="panel setup-import">
    <h2>Import your setup</h2>
    {!onCancel && <label>Harness<select value={harness} disabled={busy} onChange={event => {setHarness(event.target.value as Harness);setSelection([]);setExcluded([]);setPreview(undefined);setSaved(undefined);setError("");}}>{Object.entries(displayNames).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>}
    {!preview && !saved && <>
      <p>Bring your {displayNames[harness]} settings and skills into your Capsules.</p>
      <div className="setup-import-picker">
        <p><strong>Select your configuration folder</strong><br /><code>{harnessFolders[harness]}</code></p>
        <input ref={folderInput} hidden aria-label="Choose configuration folder" type="file" multiple {...{ webkitdirectory: "" }} disabled={busy} onChange={event => choose(event)} />
        <input ref={skillsInput} hidden aria-label="Choose skills folder" type="file" multiple {...{ webkitdirectory: "" }} disabled={busy} onChange={event => choose(event, "skills/")} />
        <input ref={filesInput} hidden aria-label="Choose configuration files" type="file" multiple disabled={busy} onChange={event => choose(event)} />
        <div className="setup-import-actions">
          <button className="primary-button" type="button" disabled={busy} onClick={() => void chooseFolder()}>Choose folder</button>
          <button type="button" disabled={busy} onClick={() => filesInput.current?.click()}>Choose individual files</button>
        </div>
        <details><summary>Skills stored somewhere else?</summary><p>Select a folder containing skill directories, such as <code>~/.agents/skills</code> or <code>~/.claude/skills</code>. They will be copied into this harness’s skills folder. Duplicate paths must be resolved before import.</p><button type="button" disabled={busy} onClick={() => void chooseFolder(true)}>Add skills folder</button></details>
        <p className="setup-import-hint">Import once, then reuse your saved setup in new Capsules. Dependency folders such as node_modules are excluded.</p>
        {!directoryPicker() && <p className="setup-import-hint">This browser’s folder picker counts every file in its confirmation, including excluded dependencies. Meridian filters them afterward; nothing is sent until Review import.</p>}
        <details><summary>Can’t find the folder?</summary><p>On macOS, press <kbd>Cmd + Shift + G</kbd> in the picker and paste <code>{harnessFolders[harness]}</code>. On Windows or Linux, use the picker’s location field or show hidden files.</p></details>
      </div>
      {harness === "claude" && <p>For global MCP settings, you can also select <code>~/.claude.json</code> individually.</p>}
      <p className="setup-import-hint">Next, review the selected files before saving. Review uploads them to your Meridian server for checks. Login caches are excluded; you’ll sign in separately.</p>
      {selection.length > 0 && <><p>{selection.length} files selected</p><ul>{selection.map(item => <li key={item.path}>{item.path} <button type="button" disabled={busy} aria-label={`Remove ${item.path}`} onClick={() => setSelection(current => current.filter(value => value.path !== item.path))}>Remove</button></li>)}</ul><button type="button" disabled={busy} onClick={() => void review()}>{busy ? "Reviewing…" : "Review import"}</button></>}
      {!selection.length && excluded.length > 0 && <p>No supported files selected. Choose the configuration folder shown above.</p>}
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
    {saved && <p role="status">Saved {saved.name}. New Capsules can now use it. <button type="button" onClick={() => setSaved(undefined)}>Import another setup</button></p>}
    {error && <p role="alert">{error}</p>}
    {onCancel && <button type="button" disabled={busy} onClick={onCancel}>Back to Capsule</button>}
  </section>;
}
