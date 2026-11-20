import type { QueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { capsuleNameFromMessage, isGeneratedThreadName } from "./threadTitle";

// A Thread started without a name gets a generated Capsule name such as
// thread-ad942540f1e7. When the operator sends its first message, save a short
// title from that message as the Capsule name so the sidebar, Needs you, and
// notifications match the header. The daemon never does this (the transcript
// is encrypted and the name is plaintext); the operator's own browser submits
// their own words through the ordinary rename, and only over a generated name.
export async function nameThreadFromFirstMessage(
  queryClient: QueryClient,
  capsuleId: string,
  message: string,
): Promise<Capsule | undefined> {
  const name = capsuleNameFromMessage(message);
  if (!name) return undefined;
  // One retry covers a Capsule that changed between the read and the rename,
  // including a second tab naming it first.
  for (let attempt = 0; attempt < 2; attempt++) {
    const current = await api.capsule(capsuleId);
    if (!isGeneratedThreadName(current.name)) return undefined;
    try {
      const next = await api.renameCapsule(current.id, name, current.resourceVersion);
      queryClient.setQueryData(queries.capsule(capsuleId).queryKey, next);
      await queryClient.invalidateQueries({ queryKey: ["capsules"] });
      return next;
    } catch (error) {
      if (normalizeAPIError(error).code !== "conflict") throw error;
    }
  }
  return undefined;
}
