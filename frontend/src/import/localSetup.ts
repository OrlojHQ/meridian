import type { HarnessSetupFile, HarnessSetupUpload } from "../api/generated/types.gen";

export class LocalSetupError extends Error {}

export type Harness = HarnessSetupUpload["harness"];
export const harnessFolders: Record<Harness, string> = { opencode: "~/.config/opencode", codex: "~/.codex", claude: "~/.claude", pi: "~/.pi/agent" };
const roots: Record<Harness, string> = { opencode: ".config/opencode", codex: ".codex", claude: ".claude", pi: ".pi/agent" };
const names: Record<Harness, string[]> = {
  opencode: ["opencode.json", "opencode.jsonc", "AGENTS.md", "agents/", "commands/", "skills/", "plugins/", "themes/", "agent/", "command/", "plugin/", "skill/"],
  codex: ["config.toml", "AGENTS.md", "AGENTS.override.md", "skills/", "prompts/", "agents/"],
  claude: ["settings.json", "CLAUDE.md", "skills/", "commands/", "agents/", "hooks/"],
  pi: ["settings.json", "models.json", "AGENTS.md", "skills/", "prompts/", "extensions/", "themes/"],
};
export type Selection = { file: File; path: string };
export type Exclusion = { path: string; reason: string };
// This filter avoids reading known authentication/runtime files in the browser.
// The server independently validates destinations and sanitizes their contents.
export function selectLocalFiles(harness: Harness, files: File[], prefix = "") {
  if (files.length > 4096) throw new LocalSetupError("This folder has too many files. Choose your harness configuration folder, rather than your home folder.");
  const included: Selection[] = [], excluded: Exclusion[] = [];
  const seen = new Set<string>();
  let bytes = 0;
  for (const file of files) {
    const relative = prefix + (file.webkitRelativePath ? file.webkitRelativePath.split("/").slice(1).join("/") : file.name);
    const parts = relative.split("/");
    const special = harness === "claude" && relative === ".claude.json";
    const allowed = special || names[harness].some(name => name.endsWith("/") ? relative.startsWith(name) : relative === name);
    const unsafe = !relative || relative.length > 1000 || /[\\\x00-\x1f\x7f]/.test(relative) || parts.some(part => {
      const name = part.toLowerCase();
      return ["", ".", "..", "auth.json", ".credentials.json", "node_modules", ".git"].includes(name) || name.startsWith(".env") || name.endsWith(".pem") || name.endsWith(".key");
    });
    if (!allowed || unsafe) { excluded.push({ path: relative, reason: "Authentication, runtime state, or unsupported file; not uploaded" }); continue; }
    const path = special ? ".claude.json" : `${roots[harness]}/${relative}`;
    if (seen.has(path)) throw new LocalSetupError(`More than one file maps to ${path}. Select each configuration file once.`);
    if (file.size > 128 * 1024) { excluded.push({ path: relative, reason: "File exceeds 128 KiB; not uploaded" }); continue; }
    seen.add(path); bytes += file.size;
    if (bytes > 512 * 1024 || included.length >= 256) throw new LocalSetupError("Choose fewer files: imports support up to 256 files and 512 KiB of configuration.");
    included.push({ file, path });
  }
  return { included, excluded };
}
export async function readLocalFiles(selection: Selection[]): Promise<HarnessSetupFile[]> {
  const result: HarnessSetupFile[] = [];
  for (const { file, path } of selection) {
    let content: string;
    try { content = new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer()); }
    catch { throw new LocalSetupError(`${file.name} could not be read as UTF-8 text. Remove it from the selection and try again.`); }
    if (content.includes("\0")) throw new LocalSetupError(`${file.name} is not a supported text file.`);
    result.push({ path, content, executable: false });
  }
  return result;
}

// Structural types keep the optional File System Access API out of the global DOM types.
export type SetupDirectory = { kind: "directory"; name: string; values(): AsyncIterable<SetupDirectory | SetupFileHandle> };
type SetupFileHandle = { kind: "file"; name: string; getFile(): Promise<File> };
type FolderWindow = Window & { showDirectoryPicker?: (options: { mode: "read" }) => Promise<SetupDirectory> };
export function directoryPicker() { return (window as FolderWindow).showDirectoryPicker?.bind(window); }

export async function scanSetupDirectory(harness: Harness, root: SetupDirectory, prefix = ""): Promise<{included: Selection[]; excluded: Exclusion[]}> {
  const included: Selection[] = [], excluded: Exclusion[] = [];
  let entries = 0, bytes = 0;
  async function walk(directory: SetupDirectory, prefix: string) {
    for await (const entry of directory.values()) {
      if (++entries > 4096) throw new LocalSetupError("This folder has too many configuration entries. Select a smaller configuration folder.");
      const relative = `${prefix}${entry.name}`;
      // Probe the existing allowlist before descending or requesting file contents.
      const probePath = entry.kind === "directory" ? `${relative}/__probe__` : relative;
      const probe = new File([], entry.name);
      Object.defineProperty(probe, "webkitRelativePath", {value: `${root.name}/${probePath}`});
      const allowed = selectLocalFiles(harness, [probe]);
      if (!allowed.included.length) {
        excluded.push({path: relative, reason: entry.kind === "directory" ? "Unsupported or dependency folder skipped without scanning its contents" : "Authentication, runtime state, or unsupported file; not read"});
        continue;
      }
      if (entry.kind === "directory") { await walk(entry, `${relative}/`); continue; }
      const file = await entry.getFile();
      Object.defineProperty(file, "webkitRelativePath", {value: `${root.name}/${relative}`});
      const selected = selectLocalFiles(harness, [file]);
      excluded.push(...selected.excluded);
      for (const item of selected.included) {
        bytes += item.file.size;
        if (included.length >= 256 || bytes > 512 * 1024) throw new LocalSetupError("Choose fewer files: imports support up to 256 files and 512 KiB of configuration.");
        included.push(item);
      }
    }
  }
  await walk(root, prefix);
  return {included, excluded};
}
