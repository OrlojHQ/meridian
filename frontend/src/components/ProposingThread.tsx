import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";

import type { NoteSource } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { threadPath } from "./NoteProposalReview";
import { leadCapsuleId, proposalSourceLabel, sourceThreadId } from "./notesText";

// ProposingThread names the Thread a proposal or promotion came from, with
// the bounded naming rules of proposerName, and links to it when asked. A
// lead is named by its Capsule, read from the Capsule the source records.
export function ProposingThread({
  source,
  origin,
  link = true,
}: {
  source: NoteSource | undefined;
  origin: "agent" | "promotion";
  link?: boolean;
}) {
  const capsule = useQuery({ ...queries.capsule(leadCapsuleId(source)), refetchInterval: false });
  const label = proposalSourceLabel(source, origin, capsule.data?.name);
  const threadId = sourceThreadId(source);
  return link && threadId ? <Link to={threadPath(threadId)}>{label}</Link> : <>{label}</>;
}
