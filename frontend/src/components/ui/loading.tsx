import { useEffect, useState, type ReactNode } from "react";

const reducedQuery = "(prefers-reduced-motion: reduce)";

export function usePrefersReducedMotion() {
  const [reduced, setReduced] = useState(
    () => typeof window !== "undefined" && window.matchMedia?.(reducedQuery).matches === true,
  );
  useEffect(() => {
    const media = window.matchMedia?.(reducedQuery);
    if (!media) return;
    const onChange = () => setReduced(media.matches);
    media.addEventListener("change", onChange);
    return () => media.removeEventListener("change", onChange);
  }, []);
  return reduced;
}

export function ActivityDot({
  tone = "active",
}: {
  tone?: "active" | "done" | "upcoming" | "failed";
}) {
  const reduced = usePrefersReducedMotion();
  const pulsing = tone === "active" && !reduced;
  return (
    <span
      className={`load-dot load-dot-${tone}${pulsing ? " is-pulsing" : ""}`}
      data-reduced-motion={reduced ? "true" : "false"}
      aria-hidden="true"
    />
  );
}

export function Skeleton({ className = "" }: { className?: string }) {
  const reduced = usePrefersReducedMotion();
  return (
    <span
      className={`skeleton${reduced ? " is-static" : ""} ${className}`.trim()}
      data-reduced-motion={reduced ? "true" : "false"}
      aria-hidden="true"
    />
  );
}

export function SkeletonBlock({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="skeleton-region" role="status" aria-busy="true" aria-label={label}>
      <span className="sr-only">{label}</span>
      {children}
    </div>
  );
}

export function WorkspaceSkeleton({ label }: { label: string }) {
  return (
    <SkeletonBlock label={label}>
      <div className="workspace-skeleton">
        <Skeleton className="skeleton-line skeleton-title" />
        <Skeleton className="skeleton-panel" />
        <Skeleton className="skeleton-line" />
        <Skeleton className="skeleton-line short" />
      </div>
    </SkeletonBlock>
  );
}

export function ListSkeleton({ label, rows = 4 }: { label: string; rows?: number }) {
  return (
    <SkeletonBlock label={label}>
      <div className="list-skeleton">
        {Array.from({ length: rows }, (_, index) => (
          <Skeleton key={index} className={`skeleton-line${index % 2 ? " short" : ""}`} />
        ))}
      </div>
    </SkeletonBlock>
  );
}

export function ProgressBar({
  label,
  value,
  max,
}: {
  label: string;
  value?: number;
  max?: number;
}) {
  const reduced = usePrefersReducedMotion();
  const determinate =
    typeof max === "number" && max > 0 && typeof value === "number" && Number.isFinite(value);
  const percent = determinate
    ? Math.max(0, Math.min(100, Math.floor((value / max) * 100)))
    : undefined;
  const shown = useMonotonicPercent(percent);
  return (
    <div
      className={`progress-track${determinate ? "" : " is-indeterminate"}${reduced ? " is-static" : ""}`}
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={determinate ? 100 : undefined}
      aria-valuenow={determinate ? shown : undefined}
      aria-valuetext={label}
      data-reduced-motion={reduced ? "true" : "false"}
    >
      <span className="progress-fill" style={determinate ? { width: `${shown}%` } : undefined} />
    </div>
  );
}

function useMonotonicPercent(target?: number) {
  const [shown, setShown] = useState(target ?? 0);
  useEffect(() => {
    if (target === undefined) return;
    setShown((current) => (target + 5 < current ? target : Math.max(current, target)));
  }, [target]);
  return shown;
}
