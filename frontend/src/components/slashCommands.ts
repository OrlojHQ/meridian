import type { ThreadBlock, ThreadCommand } from "../api/generated/types.gen";
import { safeInline } from "./threadChat";

// The daemon validates names; the UI repeats the check because the list is
// harness output and is rendered verbatim.
const COMMAND_NAME = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;
const MAX_COMMANDS = 128;
const MAX_VISIBLE = 50;

export type SlashCommand = {
  name: string;
  description: string;
  hint: string;
  kind: ThreadCommand["kind"];
  aliases: string[];
};

export type SlashControl = {
  id: "stop" | "new-session";
  label: string;
  description: string;
};

export type SlashEntry =
  | { type: "command"; key: string; command: SlashCommand; alias?: string }
  | { type: "control"; key: string; control: SlashControl };

const clean = (value: string | undefined, limit: number) => {
  const text = safeInline(value ?? "").trim();
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
};

// The adapter reports the full list whenever it changes, so the latest
// report wins. Transcripts written before discovery have none.
export function latestThreadCommands(blocks: ThreadBlock[]): SlashCommand[] | undefined {
  for (let index = blocks.length - 1; index >= 0; index -= 1) {
    const event = blocks[index].event;
    if (event?.type !== "commands") continue;
    const seen = new Set<string>();
    const commands: SlashCommand[] = [];
    for (const command of event.commands ?? []) {
      if (commands.length >= MAX_COMMANDS) break;
      if (!COMMAND_NAME.test(command.name) || seen.has(command.name)) continue;
      seen.add(command.name);
      commands.push({
        name: command.name,
        description: clean(command.description, 512),
        hint: clean(command.argumentHint, 128),
        kind: command.kind === "action" ? "action" : "prompt",
        aliases: (command.aliases ?? []).filter((alias) => COMMAND_NAME.test(alias)).slice(0, 4),
      });
    }
    return commands;
  }
  return undefined;
}

// slashQuery returns the text after "/" while the user is still typing the
// command name, and undefined once arguments start or the text is no command.
export function slashQuery(message: string): string | undefined {
  const match = /^\/(\S*)$/.exec(message);
  return match ? match[1] : undefined;
}

// Only names and aliases match: a description match would let Enter capture
// text the user meant to send as an ordinary prompt.
const rank = (query: string, candidates: string[]) => {
  let best = Number.POSITIVE_INFINITY;
  for (const candidate of candidates) {
    const value = candidate.toLowerCase();
    if (value === query) best = Math.min(best, 0);
    else if (value.startsWith(query)) best = Math.min(best, 1);
    else if (value.split(/[-_.:]/).some((part) => part.startsWith(query))) best = Math.min(best, 2);
    else if (value.includes(query)) best = Math.min(best, 3);
  }
  return best;
};

export function filterSlashEntries(
  query: string,
  commands: SlashCommand[],
  controls: SlashControl[],
): SlashEntry[] {
  const needle = query.toLowerCase();
  const ranked: { entry: SlashEntry; score: number; order: number }[] = [];
  commands.forEach((command, order) => {
    const score = needle
      ? rank(needle, [command.name, ...command.aliases])
      : 0;
    if (score === Number.POSITIVE_INFINITY) return;
    const alias = needle && !command.name.toLowerCase().includes(needle)
      ? command.aliases.find((value) => value.toLowerCase().includes(needle))
      : undefined;
    ranked.push({ entry: { type: "command", key: `command-${command.name}`, command, alias }, score, order });
  });
  ranked.sort((left, right) => left.score - right.score || left.order - right.order);
  const entries = ranked.slice(0, MAX_VISIBLE).map((item) => item.entry);
  for (const control of controls) {
    const words = [control.id, control.label];
    if (!needle || rank(needle, words) !== Number.POSITIVE_INFINITY) {
      entries.push({ type: "control", key: `control-${control.id}`, control });
    }
  }
  return entries;
}
