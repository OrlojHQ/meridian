import { useState } from "react";

// Copies a short value, such as a path or a command, and says whether it
// worked. Clipboard access can be refused on plain-HTTP remote pages, so the
// value must also stay visible for manual selection.
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setState("copied");
    } catch {
      setState("failed");
    }
  }
  return (
    <>
      <button type="button" className="copy-button" onClick={() => void copy()} aria-label={`${label}: ${text}`}>
        {state === "copied" ? "Copied" : "Copy"}
      </button>
      {state === "copied" && (
        <span className="sr-only" role="status">
          Copied
        </span>
      )}
      {state === "failed" && (
        <span className="copy-button-status" role="status">
          The browser did not allow clipboard access. Select the text instead.
        </span>
      )}
    </>
  );
}
