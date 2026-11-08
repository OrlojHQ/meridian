import type { ThreadBlock } from "../api/generated/types.gen";
import { safeInline } from "./threadText";

const MAX_MODELS = 128;
const MAX_ID = 256;

export type ThreadModelOption = { id: string; label: string; description: string };

export type ThreadModels = {
  current: string;
  // currentLabel is the matching option's label, or the raw ID when the
  // harness runs a model it does not list.
  currentLabel: string;
  options: ThreadModelOption[];
};

const clean = (value: string | undefined, limit: number) => {
  const text = safeInline(value ?? "").trim();
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
};

// The adapter reports the full list whenever it changes, so the latest report
// wins. Sessions whose harness reports no models have none, and the composer
// shows no picker. The daemon validates the list; the UI repeats the bounds
// because it is harness output rendered verbatim.
export function latestThreadModels(blocks: ThreadBlock[]): ThreadModels | undefined {
  for (let index = blocks.length - 1; index >= 0; index -= 1) {
    const event = blocks[index].event;
    if (event?.type !== "models" || !event.models) continue;
    const seen = new Set<string>();
    const options: ThreadModelOption[] = [];
    for (const option of event.models.options ?? []) {
      if (options.length >= MAX_MODELS) break;
      if (!option.id || option.id.length > MAX_ID || seen.has(option.id)) continue;
      seen.add(option.id);
      options.push({
        id: option.id,
        label: clean(option.label, 128) || clean(option.id, 128),
        description: clean(option.description, 512),
      });
    }
    const current = (event.models.current ?? "").slice(0, MAX_ID);
    const match = options.find((option) => option.id === current);
    return { current, currentLabel: match?.label ?? clean(current, 128), options };
  }
  return undefined;
}
