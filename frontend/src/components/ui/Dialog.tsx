import type { ReactNode, Ref } from "react";

import { CloseIcon } from "./Icons";

export function Dialog({
  labelledBy,
  eyebrow,
  title,
  closeLabel,
  closeRef,
  onClose,
  busy = false,
  className,
  children,
}: {
  labelledBy: string;
  eyebrow?: string;
  title: string;
  closeLabel: string;
  closeRef?: Ref<HTMLButtonElement>;
  onClose: () => void;
  busy?: boolean;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className="dialog-backdrop"
      onMouseDown={() => {
        if (!busy) onClose();
      }}
    >
      <section
        className={["launch-dialog", "ui-dialog", className].filter(Boolean).join(" ")}
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="dialog-header">
          <div>
            {eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}
            <h1 id={labelledBy}>{title}</h1>
          </div>
          <button
            ref={closeRef}
            type="button"
            className="icon-button"
            aria-label={closeLabel}
            disabled={busy}
            onClick={onClose}
          >
            <CloseIcon />
          </button>
        </header>
        <div className="dialog-body">{children}</div>
      </section>
    </div>
  );
}
