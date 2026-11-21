import { useMutation } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { Thread } from "../api/generated/types.gen";
import type { ThreadModelOption, ThreadModels, ThreadOptionSet } from "./threadModels";

function ChevronIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.6">
      <path d="m4.5 6.5 3.5 3.5 3.5-3.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="m3.5 8.5 3 3 6-7" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

type Setting = "model" | "effort";

// Long model lists (OpenCode lists every provider it is configured for) get a
// search field; short ones do not need it.
const SEARCH_THRESHOLD = 8;

// matches keeps options whose label, ID, or description contains every
// whitespace-separated term of the query, ignoring case.
function matches(option: { id: string; label: string; description: string }, query: string) {
  const haystack = `${option.label} ${option.id} ${option.description}`.toLowerCase();
  return query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((term) => haystack.includes(term));
}

type Request = { setting: Setting; id: string };

// pendingLabel is the label a requested switch will show, or undefined once
// the harness reports it (or while nothing is pending).
function pendingLabel(set: ThreadOptionSet | undefined, requested: string | undefined) {
  if (!set || requested === undefined || requested === set.current) return undefined;
  return set.options.find((option) => option.id === requested)?.label ?? requested;
}

// A reported label may name its provider as "provider · model" or
// "provider/model". The gateway id Meridian writes is not a provider the
// operator chose, so that prefix is hidden. Other providers stay, as the
// group title and on the selected-model button. Search still uses the
// reported label.
type ReportedModelLabel = { provider: string; name: string; separator: "dot" | "slash" };

// A slash is a provider separator only at the start of the label. One inside
// brackets, or with a space beside it, belongs to the model's own name, as in
// "Model X (4bit/8bit)" or "Fast / cheap".
function providerSlash(label: string) {
  const slash = label.indexOf("/");
  if (slash <= 0 || slash === label.length - 1) return -1;
  if (/[([{]/.test(label.slice(0, slash))) return -1;
  if (/\s/.test(label[slash - 1] ?? "") || /\s/.test(label[slash + 1] ?? "")) return -1;
  return slash;
}

function splitReportedModelLabel(label: string): ReportedModelLabel | undefined {
  const dot = label.indexOf(" · ");
  const slash = providerSlash(label);
  if (dot > 0 && (slash < 0 || dot < slash)) {
    const name = label.slice(dot + " · ".length);
    return name ? { provider: label.slice(0, dot), name, separator: "dot" } : undefined;
  }
  if (slash > 0) {
    return { provider: label.slice(0, slash), name: label.slice(slash + 1), separator: "slash" };
  }
  return undefined;
}

function isMeridianProvider(provider: string) {
  return provider.toLowerCase() === "meridian";
}

function visibleModelLabel(label: string) {
  const split = splitReportedModelLabel(label);
  if (!split || !isMeridianProvider(split.provider)) return label;
  return split.name;
}

// "provider · model" always names a provider. "provider/model" groups only
// when another option names the same provider, so a lone label with a slash
// in its own name is shown whole rather than under a made-up group. Providers
// are counted over every reported option, so a search that leaves one match
// does not change how it is shown.
function modelGroups(options: ThreadModelOption[], all: ThreadModelOption[] = options) {
  const splits = options.map((option) => splitReportedModelLabel(option.label));
  const providers = new Map<string, number>();
  for (const option of all) {
    const split = splitReportedModelLabel(option.label);
    if (split) providers.set(split.provider, (providers.get(split.provider) ?? 0) + 1);
  }
  const groups: { title: string; options: ThreadModelOption[] }[] = [];
  options.forEach((option, index) => {
    const split = splits[index];
    let title = "";
    let label = option.label;
    const grouped =
      split &&
      (split.separator === "dot" ||
        isMeridianProvider(split.provider) ||
        (providers.get(split.provider) ?? 0) > 1);
    if (split && grouped) {
      title = isMeridianProvider(split.provider) ? "" : split.provider;
      label = split.name;
    }
    const shown = { ...option, label };
    const current = groups[groups.length - 1];
    if (current && current.title === title) current.options.push(shown);
    else groups.push({ title, options: [shown] });
  });
  return groups;
}

function OptionGroup({
  title,
  set,
  selected,
  onChoose,
}: {
  title: string;
  set: ThreadOptionSet;
  selected: string;
  onChoose: (id: string) => void;
}) {
  return (
    <div role="group" aria-label={title}>
      <p className="slash-group">{title}</p>
      <ul>
        {set.options.map((option) => (
          <li key={option.id}>
            <button
              type="button"
              role="menuitemradio"
              aria-checked={option.id === selected}
              className="composer-model-option"
              onClick={() => onChoose(option.id)}
            >
              <span className="composer-model-option-text">
                <span className="composer-model-option-label">{option.label}</span>
                {option.description && (
                  <span className="composer-model-option-description">{option.description}</span>
                )}
              </span>
              {option.id === selected && <CheckIcon />}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

// Arrow keys move through the open menu. Modifiers keep the search field's
// own caret behavior.
function moveMenuFocus(event: KeyboardEvent<HTMLElement>) {
  if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  const items = [
    ...event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]'),
  ];
  if (items.length === 0) return;
  event.preventDefault();
  const index = items.indexOf(document.activeElement as HTMLButtonElement);
  const next =
    index === -1
      ? event.key === "ArrowDown"
        ? 0
        : items.length - 1
      : index + (event.key === "ArrowDown" ? 1 : -1);
  items[(next + items.length) % items.length]?.focus();
}

// The model picker sits inside the message box. It lists only what the
// session's harness reported: a model button and, when the harness reports
// more than one reasoning-effort level for the current model, a separate
// effort button. Each opens its own menu. Only the model menu scrolls. A
// change applies from the next turn, and the harness reports its models again
// once it has applied it.
export function ThreadModelPicker({
  thread,
  models,
  disabled,
  onSwitched,
  onConflict,
}: {
  thread: Thread;
  models: ThreadModels;
  disabled: boolean;
  onSwitched: () => void;
  onConflict: () => Promise<void>;
}) {
  const [open, setOpen] = useState<Setting | null>(null);
  const [query, setQuery] = useState("");
  const [requestedModel, setRequestedModel] = useState<string>();
  const [requestedEffort, setRequestedEffort] = useState<string>();
  const modelMenuId = useId();
  const effortMenuId = useId();
  const root = useRef<HTMLDivElement>(null);
  const effort = models.effort && models.effort.options.length > 1 ? models.effort : undefined;
  const mutation = useMutation({
    mutationFn: ({ setting, id }: Request) =>
      setting === "model"
        ? api.setThreadModel(thread.id, thread.resourceVersion, id)
        : api.setThreadEffort(thread.id, thread.resourceVersion, id),
    onSuccess: () => onSwitched(),
    onError: async (error, { setting }) => {
      if (setting === "model") setRequestedModel(undefined);
      else setRequestedEffort(undefined);
      if (normalizeAPIError(error).code === "conflict") await onConflict();
    },
  });
  // Once the harness reports what it switched to, stop showing the request
  // as pending.
  useEffect(() => {
    if (requestedModel !== undefined && models.current === requestedModel) setRequestedModel(undefined);
  }, [models.current, requestedModel]);
  useEffect(() => {
    if (requestedEffort !== undefined && effort?.current === requestedEffort) setRequestedEffort(undefined);
  }, [effort, requestedEffort]);
  useEffect(() => {
    if (open === "model" && models.options.length === 0) setOpen(null);
    if (open === "effort" && !effort) setOpen(null);
  }, [open, effort, models.options.length]);
  useEffect(() => {
    if (open !== "model") setQuery("");
    if (!open) return;
    const onPointer = (event: PointerEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) setOpen(null);
    };
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") setOpen(null);
    };
    document.addEventListener("pointerdown", onPointer);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointer);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  if (models.options.length === 0 && !models.current) return null;
  const modelPending = pendingLabel(models, requestedModel);
  const effortPending = pendingLabel(effort, requestedEffort);
  // The button names the model that runs, not the alias that picks it.
  const modelShown = visibleModelLabel(modelPending ?? (models.resolved || models.currentLabel || "Model"));
  // A report with levels but no current still names the level the Thread
  // started with, or the last level chosen. Default is the harness's own
  // level when even that is unknown. Effort is only the last resort.
  const effortShown = effortPending ?? (effort?.currentLabel || (effort ? "Default" : ""));
  const searchable = models.options.length > SEARCH_THRESHOLD;
  const shownModels =
    searchable && query ? models.options.filter((option) => matches(option, query)) : models.options;
  const choose = (setting: Setting, id: string) => {
    setOpen(null);
    const current = setting === "model" ? models.current : effort?.current;
    if (id === current || mutation.isPending) return;
    if (setting === "model") setRequestedModel(id);
    else setRequestedEffort(id);
    mutation.mutate({ setting, id });
  };
  const toggle = (setting: Setting) => setOpen((current) => (current === setting ? null : setting));
  const modelSwitching = modelPending !== undefined;
  const effortSwitching = effortPending !== undefined;
  return (
    <div className="composer-model" ref={root} onKeyDown={moveMenuFocus}>
      <button
        type="button"
        className="composer-model-button"
        aria-haspopup="menu"
        aria-expanded={open === "model"}
        aria-controls={open === "model" ? modelMenuId : undefined}
        aria-label={`Model: ${modelShown}${modelSwitching ? ", switching" : ""}`}
        title={modelSwitching ? "Switching from the next turn" : "Model for the next turn"}
        disabled={disabled || models.options.length === 0}
        onClick={() => toggle("model")}
      >
        <span className={modelSwitching ? "composer-model-label shimmer" : "composer-model-label"}>
          {modelShown}
        </span>
        <ChevronIcon />
      </button>
      {effort && (
        <button
          type="button"
          className="composer-model-button composer-effort-button"
          aria-haspopup="menu"
          aria-expanded={open === "effort"}
          aria-controls={open === "effort" ? effortMenuId : undefined}
          aria-label={
            effortSwitching
              ? `Reasoning effort: ${effortPending}, switching`
              : effortShown
                ? `Reasoning effort: ${effortShown}`
                : "Reasoning effort"
          }
          title={effortSwitching ? "Switching from the next turn" : "Reasoning effort for the next turn"}
          disabled={disabled}
          onClick={() => toggle("effort")}
        >
          <span className={effortSwitching ? "composer-model-label shimmer" : "composer-model-label"}>
            {effortShown || "Effort"}
          </span>
          <ChevronIcon />
        </button>
      )}
      {open === "model" && (
        <div className="composer-model-menu" id={modelMenuId} role="menu" aria-label="Models">
          {searchable && (
            <input
              className="composer-model-search"
              type="search"
              aria-label="Search models"
              placeholder="Search models"
              autoComplete="off"
              spellCheck={false}
              autoFocus
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => {
                // Enter picks the first match, so a few letters and Enter
                // switch models without the pointer. Always consume Enter so
                // the surrounding message form does not send.
                if (event.key !== "Enter") return;
                event.preventDefault();
                if (shownModels[0]) choose("model", shownModels[0].id);
              }}
            />
          )}
          <div className="composer-model-scroll">
            {models.options.length > 0 &&
              (shownModels.length > 0 ? (
                modelGroups(shownModels, models.options).map((group) => (
                  <OptionGroup
                    key={group.title || "models"}
                    title={group.title || "Model · applies from the next turn"}
                    set={{ ...models, options: group.options }}
                    selected={requestedModel ?? models.current}
                    onChoose={(id) => choose("model", id)}
                  />
                ))
              ) : (
                <p className="composer-model-empty">No models match “{query}”</p>
              ))}
          </div>
        </div>
      )}
      {open === "effort" && effort && (
        <div
          className="composer-model-menu composer-model-menu-effort"
          id={effortMenuId}
          role="menu"
          aria-label="Reasoning effort"
        >
          <OptionGroup
            title="Reasoning effort · applies from the next turn"
            set={effort}
            selected={requestedEffort ?? effort.current}
            onChoose={(id) => choose("effort", id)}
          />
        </div>
      )}
      {mutation.isError && (
        <p role="alert" className="composer-model-error">
          {normalizeAPIError(mutation.error).message}
        </p>
      )}
    </div>
  );
}
