import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import type { CoordinationRequest } from "../api/generated/types.gen";

export function SpawnDecision({ request }: { request: CoordinationRequest }) {
  const queryClient = useQueryClient();
  const invalidate = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["coordination-requests"] }),
      queryClient.invalidateQueries({ queryKey: ["thread-links"] }),
      queryClient.invalidateQueries({ queryKey: ["board"] }),
      queryClient.invalidateQueries({ queryKey: ["coordination-grant"] }),
    ]);
  };
  const approve = useMutation({
    mutationFn: () => api.approveCoordinationRequest(request),
    onSettled: invalidate,
  });
  const deny = useMutation({
    mutationFn: () => api.denyCoordinationRequest(request),
    onSettled: invalidate,
  });
  const pending = approve.isPending || deny.isPending;
  const error = approve.error ?? deny.error;
  return (
    <div className="actions spawn-decision">
      <button type="button" disabled={pending} onClick={() => approve.mutate()}>
        Approve spawn
      </button>
      <button type="button" className="danger" disabled={pending} onClick={() => deny.mutate()}>
        Deny
      </button>
      {error && (
        <p role="alert" className="inline-error">{normalizeAPIError(error).message}</p>
      )}
    </div>
  );
}
