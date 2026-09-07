export function StateBadge({ state }: { state: string }) {
  return (
    <span className={`badge state-${state.toLowerCase()}`}>
      {state}
    </span>
  );
}
