// The Thread composer keeps its unsent text in memory. Switching Station
// reloads the window, so the shell asks before throwing that text away.

const holders = new Set<symbol>();

export function trackComposerDraft(token: symbol, pending: boolean) {
  if (pending) holders.add(token);
  else holders.delete(token);
}

export function hasComposerDraft() {
  return holders.size > 0;
}

export function clearComposerDrafts() {
  holders.clear();
}
