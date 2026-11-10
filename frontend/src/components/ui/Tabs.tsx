import type { ReactNode } from "react";

export function Tabs({
  label,
  value,
  onChange,
  tabs,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  tabs: { id: string; label: string; icon?: ReactNode }[];
}) {
  return (
    <div className="ui-tabs segmented-control" role="tablist" aria-label={label}>
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
