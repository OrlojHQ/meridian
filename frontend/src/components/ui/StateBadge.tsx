import { Badge } from "./Badge";

export function StateBadge({ state }: { state: string }) {
  return <Badge tone={state.toLowerCase()}>{state}</Badge>;
}
