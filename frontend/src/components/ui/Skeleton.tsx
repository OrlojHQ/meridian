export function Skeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div className="ui-skeleton" aria-hidden="true">
      {Array.from({ length: lines }, (_, index) => (
        <span
          key={index}
          className="ui-skeleton-line"
          {...(index === lines - 1 ? { "data-short": "" } : {})}
        />
      ))}
    </div>
  );
}

export function LoadingState({ label, lines = 3 }: { label: string; lines?: number }) {
  return (
    <div className="loading" role="status">
      <Skeleton lines={lines} />
      <span className="sr-only">{label}</span>
    </div>
  );
}
