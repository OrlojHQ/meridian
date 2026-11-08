import { useMutation } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { Thread } from "../api/generated/types.gen";
import type { ThreadModels } from "./threadModels";

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

// The model picker sits inside the message box. It lists only what the
// session's harness reported; the switch applies from the next turn, and the
// harness reports its models again once it has switched.
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
  const [requested, setRequested] = useState<string>();
  const menuId = useId();
  const root = useRef<HTMLDivElement>(null);
  const mutation = useMutation({
    mutationFn: (model: string) => api.setThreadModel(thread.id, thread.resourceVersion, model),
    onSuccess: () => onSwitched(),
    onError: async (error) => {
      setRequested(undefined);
      if (normalizeAPIError(error).code === "conflict") await onConflict();
    },
  });
  // Once the harness reports the model it switched to, stop showing the
  // request as pending.
  useEffect(() => {
    if (requested !== undefined && models.current === requested) setRequested(undefined);
  }, [models.current, requested]);
  useEffect(() => {
    if (!open) return;
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
  const pending = requested !== undefined && requested !== models.current;
  const shown = pending
    ? (models.options.find((option) => option.id === requested)?.label ?? requested)
    : models.currentLabel || "Model";
  const choose = (id: string) => {
    setOpen(false);
    if (id === models.current || mutation.isPending) return;
    setRequested(id);
    mutation.mutate(id);
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
        disabled={disabled || models.options.length === 0}
        onClick={() => setOpen((value) => !value)}
      >
        <span className={pending ? "composer-model-label shimmer" : "composer-model-label"}>{shown}</span>
        <ChevronIcon />
      </button>
      {open && (
        <div className="composer-model-menu" id={menuId} role="menu" aria-label="Models">
          <p className="slash-group">Model · applies from the next turn</p>
          <ul>
            {models.options.map((option) => {
              const selected = option.id === (pending ? requested : models.current);
              return (
                <li key={option.id}>
                  <button
                    type="button"
                    role="menuitemradio"
                    aria-checked={selected}
                    className="composer-model-option"
                    onClick={() => choose(option.id)}
                  >
                    <span className="composer-model-option-text">
                      <span className="composer-model-option-label">{option.label}</span>
                      {option.description && (
                        <span className="composer-model-option-description">{option.description}</span>
                      )}
                    </span>
                    {selected && <CheckIcon />}
                  </button>
                </li>
              );
            })}
          </ul>
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
