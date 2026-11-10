import type { ReactNode } from "react";

export function Badge({
  children,
  tone,
  className,
}: {
  children: ReactNode;
  tone?: string;
  className?: string;
}) {
  return (
    <span className={["ui-badge", "badge", tone ? `state-${tone}` : "", className].filter(Boolean).join(" ")}>
      {children}
    </span>
  );
}
