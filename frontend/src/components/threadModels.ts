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
  // images is true when the harness accepts image content.
  images: boolean;
  // effort is the current model's reasoning effort levels, when the harness
  // reports any. An empty current means the harness did not say which; the
  // composer then uses the level the Thread started with or the last level
  // the operator chose.
  effort?: ThreadOptionSet;
};

// Canonical effort scale. A level the current model does not offer is
// clamped along it.
const EFFORT_RANK = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];

// clampEffortID keeps want when this model offers it. Otherwise it is the
// highest offered level that does not exceed want, or the lowest offered
// level when want sits below all of them.
export function clampEffortID(ids: string[], want: string): string {
  if (!want) return "";
  if (ids.includes(want)) return want;
  const wantRank = EFFORT_RANK.indexOf(want);
  const ranked = ids
    .map((id) => ({ id, rank: EFFORT_RANK.indexOf(id) }))
    .filter((item) => item.rank >= 0);
  if (wantRank < 0 || ranked.length === 0) return ids[0] ?? "";
  const below = ranked.filter((item) => item.rank <= wantRank);
  if (below.length > 0) {
    return below.reduce((best, item) => (item.rank > best.rank ? item : best)).id;
  }
  return ranked.reduce((best, item) => (item.rank < best.rank ? item : best)).id;
}

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

// rememberedEffort is the level the Thread was started with, or the last
// level the operator chose, when the latest report does not name one. A
// refused change does not count. A report with a single level is not a
// choice the composer showed.
function rememberedEffort(blocks: ThreadBlock[], latestIndex: number): string {
  let known = "";
  let pending = "";
  for (let index = 0; index < blocks.length; index += 1) {
    const event = blocks[index].event;
    if (!event) continue;
    if (event.type === "set_effort" && event.effort) {
      pending = event.effort.slice(0, MAX_ID);
      continue;
    }
    if (event.type === "error" && event.code === "effort_unavailable") {
      pending = "";
      continue;
    }
    if (index === latestIndex || event.type !== "models" || !event.models?.effort?.current) continue;
    if ((event.models.effort.options?.length ?? 0) <= 1) continue;
    known = event.models.effort.current.slice(0, MAX_ID);
    pending = "";
  }
  return pending || known;
}

export function latestThreadModels(blocks: ThreadBlock[]): ThreadModels | undefined {
  let latestIndex = -1;
  for (let index = blocks.length - 1; index >= 0; index -= 1) {
    const event = blocks[index].event;
    if (event?.type === "models" && event.models) {
      latestIndex = index;
      break;
    }
  }
  if (latestIndex < 0) return undefined;
  const event = blocks[latestIndex].event;
  if (!event?.models) return undefined;
  const models: ThreadModels = {
    ...optionSet(event.models, MAX_MODELS),
    resolved: clean(event.models.resolved, 128),
    images: event.models.images === true,
  };
  if (event.models.effort) {
    const effort = optionSet(event.models.effort, MAX_EFFORTS);
    // One level is not a choice.
    if (effort.options.length > 1) {
      if (!effort.current) {
        const known = rememberedEffort(blocks, latestIndex);
        const id = clampEffortID(
          effort.options.map((option) => option.id),
          known,
        );
        const match = effort.options.find((option) => option.id === id);
        if (match) {
          effort.current = id;
          effort.currentLabel = match.label;
        }
      }
      models.effort = effort;
    }
  }
  return models;
}
