import { useMutation } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { Thread } from "../api/generated/types.gen";
import type { ThreadModels, ThreadOptionSet } from "./threadModels";

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

// The model picker sits inside the message box. It lists only what the
// session's harness reported: its models and, when the harness reports them,
// the current model's reasoning effort levels. A change applies from the next
// turn, and the harness reports its models again once it has applied it.
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
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [requestedModel, setRequestedModel] = useState<string>();
  const [requestedEffort, setRequestedEffort] = useState<string>();
  const menuId = useId();
  const root = useRef<HTMLDivElement>(null);
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
    if (requestedEffort !== undefined && models.effort?.current === requestedEffort) setRequestedEffort(undefined);
  }, [models.effort?.current, requestedEffort]);
  useEffect(() => {
    if (!open) {
      setQuery("");
      return;
    }
    const onPointer = (event: PointerEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) setOpen(false);
    };
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
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
  const effortPending = pendingLabel(models.effort, requestedEffort);
  const pending = modelPending !== undefined || effortPending !== undefined;
  const modelShown = modelPending ?? (models.currentLabel || "Model");
  const effortShown = effortPending ?? models.effort?.currentLabel;
  const shown = effortShown ? `${modelShown} · ${effortShown}` : modelShown;
  const searchable = models.options.length > SEARCH_THRESHOLD;
  const shownModels = searchable && query ? models.options.filter((option) => matches(option, query)) : models.options;
  const choose = (setting: Setting, id: string) => {
    setOpen(false);
    const current = setting === "model" ? models.current : models.effort?.current;
    if (id === current || mutation.isPending) return;
    if (setting === "model") setRequestedModel(id);
    else setRequestedEffort(id);
    mutation.mutate({ setting, id });
  };
  return (
    <div className="composer-model" ref={root}>
      <button
        type="button"
        className="composer-model-button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={`Model: ${shown}${pending ? ", switching" : ""}`}
        title={pending ? "Switching from the next turn" : "Model for the next turn"}
        disabled={disabled || (models.options.length === 0 && !models.effort)}
        onClick={() => setOpen((value) => !value)}
      >
        <span className={pending ? "composer-model-label shimmer" : "composer-model-label"}>{shown}</span>
        <ChevronIcon />
      </button>
      {open && (
        <div className="composer-model-menu" id={menuId} role="menu" aria-label="Models">
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
                // switch models without the pointer.
                if (event.key === "Enter" && shownModels[0]) {
                  event.preventDefault();
                  choose("model", shownModels[0].id);
                }
              }}
            />
          )}
          {models.options.length > 0 &&
            (shownModels.length > 0 ? (
              <OptionGroup
                title="Model · applies from the next turn"
                set={{ ...models, options: shownModels }}
                selected={requestedModel ?? models.current}
                onChoose={(id) => choose("model", id)}
              />
            ) : (
              <p className="composer-model-empty">No models match “{query}”</p>
            ))}
          {models.effort && (
            <OptionGroup
              title="Reasoning effort"
              set={models.effort}
              selected={requestedEffort ?? models.effort.current}
              onChoose={(id) => choose("effort", id)}
            />
          )}
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
