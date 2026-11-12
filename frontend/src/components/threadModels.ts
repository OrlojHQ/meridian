import type { ThreadBlock } from "../api/generated/types.gen";
import { safeInline } from "./threadText";

const MAX_MODELS = 128;
const MAX_EFFORTS = 16;
const MAX_ID = 256;

export type ThreadModelOption = { id: string; label: string; description: string };

export type ThreadOptionSet = {
  current: string;
  // currentLabel is the matching option's label, or the raw ID when the
  // harness runs a model it does not list.
  currentLabel: string;
  options: ThreadModelOption[];
};

export type ThreadModels = ThreadOptionSet & {
  // resolved names the model the current option runs, such as "Opus 5.5"
  // behind a "default" alias, when the harness reports it; otherwise empty.
  resolved: string;
  // effort is the current model's reasoning effort levels, when the harness
  // reports any. An empty current means the harness did not say which.
  effort?: ThreadOptionSet;
};

const clean = (value: string | undefined, limit: number) => {
  const text = safeInline(value ?? "").trim();
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
};

// The adapter reports the full list whenever it changes, so the latest report
// wins. Sessions whose harness reports no models have none, and the composer
// shows no picker. The daemon validates the list; the UI repeats the bounds
// because it is harness output rendered verbatim.
function optionSet(
  raw: { current?: string; options?: { id: string; label: string; description?: string }[] },
  limit: number,
): ThreadOptionSet {
  const seen = new Set<string>();
  const options: ThreadModelOption[] = [];
  for (const option of raw.options ?? []) {
    if (options.length >= limit) break;
    if (!option.id || option.id.length > MAX_ID || seen.has(option.id)) continue;
    seen.add(option.id);
    options.push({
      id: option.id,
      label: clean(option.label, 128) || clean(option.id, 128),
      description: clean(option.description, 512),
    });
  }
  const current = (raw.current ?? "").slice(0, MAX_ID);
  const match = options.find((option) => option.id === current);
  return { current, currentLabel: match?.label ?? clean(current, 128), options };
}

export function latestThreadModels(blocks: ThreadBlock[]): ThreadModels | undefined {
  for (let index = blocks.length - 1; index >= 0; index -= 1) {
    const event = blocks[index].event;
    if (event?.type !== "models" || !event.models) continue;
    const models: ThreadModels = {
      ...optionSet(event.models, MAX_MODELS),
      resolved: clean(event.models.resolved, 128),
    };
    if (event.models.effort) {
      const effort = optionSet(event.models.effort, MAX_EFFORTS);
      // One level is not a choice.
      if (effort.options.length > 1) models.effort = effort;
    }
    return models;
  }
  return undefined;
}
