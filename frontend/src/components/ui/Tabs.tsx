import type { ReactNode } from "react";

export function Tabs({
  label,
  value,
  onChange,
  tabs,
  variant = "segmented",
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  tabs: { id: string; label: string; icon?: ReactNode }[];
  // "underline" is a quiet row of text tabs for compact headers, where a
  // segmented control would outweigh the content below it.
  variant?: "segmented" | "underline";
}) {
  const style = variant === "underline" ? "underline-tabs" : "segmented-control";
  return (
    <div className={`ui-tabs ${style}`} role="tablist" aria-label={label}>
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          role="tab"
          aria-selected={value === tab.id}
          className={value === tab.id ? "active" : undefined}
          onClick={() => onChange(tab.id)}
        >
          {tab.icon}
          {tab.label}
        </button>
      ))}
    </div>
  );
}
