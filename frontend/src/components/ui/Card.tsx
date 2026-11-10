import type { HTMLAttributes } from "react";

export function Card({ className, ...props }: HTMLAttributes<HTMLElement>) {
  return <section className={["ui-card", className].filter(Boolean).join(" ")} {...props} />;
}
