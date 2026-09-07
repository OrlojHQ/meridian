import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, normalizeAPIError } from "../api/client";
import type { HarnessSetup, HarnessSetupPreview, ImportHarnessSetupRequest } from "../api/generated/types.gen";

export function HarnessSetupEditor({setup, onClose}: {setup: HarnessSetup; onClose: () => void}) {
  const cache = useQueryClient();
  const [draft, setDraft] = useState<ImportHarnessSetupRequest>();
  const [preview, setPreview] = useState<HarnessSetupPreview>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [accepted, setAccepted] = useState(false);
  const [skillName, setSkillName] = useState("");
  const [filePath, setFilePath] = useState("");
  const attempt = useRef<{fingerprint: string; key: string} | undefined>(undefined);
  useEffect(() => {
    let active = true;
    // Contents live only in the editor, never the query cache or local storage.
    void api.harnessSetupContents(setup.id).then(value => { if (active) setDraft(value); }).catch(cause => { if (active) setError(normalizeAPIError(cause).message); });
    return () => { active = false; };
  }, [setup.id]);
  const roots: Record<string, string> = {opencode: ".config/opencode", codex: ".codex", claude: ".claude", pi: ".pi/agent"};
  function addFile(path: string, content: string) {
    if (!draft) return;
    if (draft.bundle.files.some(file => file.path === path)) {setError("A file already exists at that path."); return;}
    if (draft.bundle.files.length >= 256) {setError("A setup can contain at most 256 files."); return;}
    setDraft({...draft, bundle: {...draft.bundle, files: [...draft.bundle.files, {path, content, executable: false}]}});
    setError(""); setSkillName(""); setFilePath("");
  }
  async function review() {
    if (!draft) return;
    setBusy(true); setError("");
    try {
      const result = await api.previewHarnessSetup({harness: draft.bundle.harness, files: draft.bundle.files});
      result.bundle.dependencies = [...new Set([...(draft.bundle.dependencies ?? []), ...(result.bundle.dependencies ?? [])])].sort();
      setPreview(result); setAccepted(false);
    }
    catch (cause) { setError(normalizeAPIError(cause).message); }
    finally { setBusy(false); }
  }
  async function save() {
    if (!draft || !preview) return;
    setBusy(true); setError("");
    try {
      const input = {...draft, bundle: preview.bundle};
      const fingerprint = JSON.stringify(input);
      if (attempt.current?.fingerprint !== fingerprint) attempt.current = {fingerprint, key: crypto.randomUUID()};
      await api.importHarnessSetup(input, attempt.current.key);
      await cache.invalidateQueries({queryKey: ["harness-setups"]});
      onClose();
    } catch (cause) { setError(normalizeAPIError(cause).message); }
    finally { setBusy(false); }
  }
  return <section className="setup-import setup-editor" aria-label={`Edit ${setup.name}`}>
    <h3>Edit saved setup</h3>
    <p>Changes create a new revision for future Capsules. Existing Capsules keep their setup.</p>
    {!draft && !error && <p role="status">Loading configuration…</p>}
    {draft && !preview && <>
      {draft.bundle.files.map((file, index) => <details key={file.path}><summary>{file.path}</summary>
        <label>Contents of {file.path}<textarea spellCheck={false} rows={12} value={file.content} disabled={busy} onChange={event => {const content = event.target.value; setDraft({...draft, bundle: {...draft.bundle, files: draft.bundle.files.map((item,i) => i === index ? {...item,content} : item)}});}} /></label>
        <button type="button" disabled={busy} onClick={() => setDraft({...draft, bundle: {...draft.bundle, files: draft.bundle.files.filter((_,i) => i !== index)}})}>Remove {file.path}</button>
      </details>)}
      <div className="setup-import-picker"><h4>Add a skill</h4>
        <label>Skill name<input placeholder="review-code" value={skillName} disabled={busy} onChange={event => setSkillName(event.target.value)} /></label>
        <button type="button" disabled={busy || !/^[a-z0-9]+(-[a-z0-9]+)*$/.test(skillName) || skillName.length > 64} onClick={() => addFile(`${roots[draft.bundle.harness]}/skills/${skillName}/SKILL.md`, `---\nname: ${skillName}\ndescription: Describe when to use this skill.\n---\n\nWrite the instructions for this skill here.\n`)}>Add skill</button>
      </div>
      <details><summary>Add another configuration file</summary><p>Use a path relative to <code>{roots[draft.bundle.harness]}</code>. Only supported configuration paths can be saved.</p>
        <label>File path<input value={filePath} disabled={busy} placeholder="AGENTS.md" onChange={event => setFilePath(event.target.value)} /></label>
        <button type="button" disabled={busy || !filePath.trim()} onClick={() => addFile(`${roots[draft.bundle.harness]}/${filePath.trim()}`, "")}>Add file</button>
      </details>
      <button type="button" disabled={busy || !draft.bundle.files.length} onClick={() => void review()}>{busy ? "Checking…" : "Review changes"}</button>
    </>}
    {preview && <>
      <h4>Review the files that will be saved</h4>
      {preview.bundle.files.map(file => <details key={file.path}><summary>{file.path}</summary><pre className="setup-import-content">{file.content}</pre></details>)}
      {!!preview.bundle.dependencies?.length && <><h4>Tools included in this setup</h4><ul>{preview.bundle.dependencies.map(pkg => <li key={pkg}>{pkg}</li>)}</ul></>}
      {!!preview.warnings?.length && <><h4>Portability warnings — settings kept</h4><ul>{preview.warnings.map((warning,i) => <li key={i}>{warning.path}: {warning.reason}</li>)}</ul></>}
      {!!preview.issues.length && <><ul>{preview.issues.map((issue,i) => <li key={i}>{issue.path}: {issue.reason}</li>)}</ul><label><input type="checkbox" checked={accepted} onChange={event => setAccepted(event.target.checked)} disabled={busy} />I reviewed the exclusions</label></>}
      <div className="setup-import-actions"><button type="button" className="primary-button" disabled={busy || !preview.bundle.files.length || (!!preview.issues.length && !accepted)} onClick={() => void save()}>{busy ? "Saving…" : "Save changes"}</button>
      <button type="button" disabled={busy} onClick={() => setPreview(undefined)}>Back to editing</button></div>
    </>}
    {error && <p role="alert">{error}</p>}
    <button type="button" disabled={busy} onClick={onClose}>Close editor</button>
  </section>;
}
