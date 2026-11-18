import { useQueries } from "@tanstack/react-query";
import { createContext, useContext } from "react";

import type { Thread, ThreadLink } from "../api/generated/types.gen";
import { queries } from "../api/queries";

// A lead's subagents can open as tabs on the lead Capsule's page. Opened tabs
// live in the URL as a repeatable `open=` parameter beside `session=`, so they
// survive reload and back/forward without browser storage. The URL is only a
// request: a tab opens only for a Thread that a lead Thread in this Capsule
// actually linked as a subagent.
export const OPEN_PARAM = "open";

// More ids than any grant can link are noise; the rest are ignored.
const MAX_OPEN = 32;

export function openSubagentIds(params: URLSearchParams) {
  const ids: string[] = [];
  for (const id of params.getAll(OPEN_PARAM)) {
    if (id && !ids.includes(id)) ids.push(id);
    if (ids.length === MAX_OPEN) break;
  }
  return ids;
}

// capsuleSearch builds the Capsule page's query string for a selected session
// and the opened subagent tabs, in their order.
export function capsuleSearch(session: string, open: readonly string[]) {
  const params = new URLSearchParams();
  if (session) params.set("session", session);
  for (const id of open) params.append(OPEN_PARAM, id);
  const search = params.toString();
  return search ? `?${search}` : "";
}

// Lead Threads hold a coordination grant at the root of a tree. A subagent's
// Thread names its lead, so it is never a lead itself.
export function leadThreadIds(threads: readonly Thread[] | undefined) {
  return (threads ?? [])
    .filter((thread) => thread.coordinationGrantId && !thread.parentThreadId && thread.state !== "deleted")
    .map((thread) => thread.id);
}

// useLinkedSubagents returns the requested subagent tabs that a lead Thread in
// this Capsule really linked, in the requested order. It asks the daemon only
// while something is requested; `pending` is true until it can tell.
export function useLinkedSubagents(
  capsuleId: string,
  threads: readonly Thread[] | undefined,
  requested: readonly string[],
) {
  const wanted = requested.length > 0;
  const leads = wanted ? leadThreadIds(threads) : [];
  const results = useQueries({
    queries: leads.map((id) => queries.threadLinks(id)),
  });
  const linked = new Map<string, ThreadLink>();
  results.forEach((result, index) => {
    for (const link of result.data?.items ?? []) {
      if (link.parentThreadId !== leads[index] || link.parentCapsuleId !== capsuleId) continue;
      if (!link.childThreadId || link.childCapsuleId === capsuleId) continue;
      linked.set(link.childThreadId, link);
    }
  });
  const tabs = requested.flatMap((id) => {
    const link = linked.get(id);
    return link ? [link] : [];
  });
  const pending = wanted && (threads === undefined || results.some((result) => result.isPending));
  return { tabs, pending };
}

// The lead Capsule page provides this so a subagent row opens a tab there
// instead of leaving the page. Elsewhere (a Thread on its own page) it is
// absent and rows link to the subagent's Capsule page as before.
export type SubagentTabs = {
  capsuleId: string;
  /** Href that opens the subagent's Thread as a tab and selects it. */
  openHref: (childThreadId: string) => string;
  /** Href that selects one of the page's sessions, keeping open tabs. */
  sessionHref: (sessionId: string) => string;
};

export const SubagentTabsContext = createContext<SubagentTabs | null>(null);

export function useSubagentTabs() {
  return useContext(SubagentTabsContext);
}
