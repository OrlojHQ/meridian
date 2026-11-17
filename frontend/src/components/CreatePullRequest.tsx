import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule, Delivery } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { diffStats, parseUnifiedDiff } from "./DiffViewer";
import { checkBanner } from "../shell/checks";
import { changeSummary, suggestedBranch } from "../shell/deliveryDraft";

export function ShipPanel({
  capsule,
  mode = "create",
  existingBranch = "",
  suggestion,
  onCreated,
}: {
  capsule: Capsule;
  mode?: "create" | "update";
  existingBranch?: string;
  suggestion?: { title: string; body: string };
  onCreated?: (delivery: Delivery) => void;
}) {
  const queryClient = useQueryClient();
  const inspection = useQuery({
    ...queries.deliveryInspection(capsule.id),
    enabled: capsule.state === "Ready",
  });
  const diff = useQuery({
    ...queries.gitDiff(capsule.id),
    enabled: capsule.state === "Ready",
  });
  const [branch, setBranch] = useState(existingBranch);
  const [title, setTitle] = useState(suggestion?.title ?? "");
  const [body, setBody] = useState(suggestion?.body ?? "");
  const [base, setBase] = useState("");
  const [commitMessage, setCommitMessage] = useState(suggestion?.title ?? "");
  const [pushOnly, setPushOnly] = useState(mode === "update");
  const [seeded, setSeeded] = useState(false);
  if (inspection.data && !seeded) {
    setSeeded(true);
    if (!existingBranch) {
      setBranch(suggestedBranch(inspection.data.branch, inspection.data.defaultBranch, suggestion?.title ?? ""));
    }
    if (!base) setBase(inspection.data.defaultBranch ?? "");
  }
  const mutation = useMutation({
    mutationFn: () => {
      if (!inspection.data) throw new Error("Git state is not ready");
      const remoteBranch = branch.trim();
      const opening = mode === "create" && !pushOnly;
      const message = inspection.data.dirty
        ? (opening ? title.trim() : commitMessage.trim())
        : "";
      return api.createDelivery(capsule.id, {
        action: opening ? "open_pull_request" : "push",
        approved: true,
        expectedResourceVersion: inspection.data.capsuleResourceVersion,
        expectedHead: inspection.data.head,
        expectedTree: inspection.data.tree,
        remoteBranch,
        ...(message ? { commitMessage: message } : {}),
        ...(opening
          ? {
              pullRequestTitle: title.trim(),
              ...(body.trim() ? { pullRequestBody: body } : {}),
              ...(base.trim() ? { baseBranch: base.trim() } : {}),
            }
          : {}),
      });
    },
    onSuccess: async (delivery) => {
      onCreated?.(delivery);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["deliveries", capsule.id] }),
        queryClient.invalidateQueries({ queryKey: ["delivery-inspection", capsule.id] }),
      ]);
    },
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSubmit) return;
    mutation.mutate();
  }

  const opening = mode === "create" && !pushOnly;
  const dirty = inspection.data?.dirty === true;
  const ahead = Boolean(
    inspection.data?.branch &&
      inspection.data.defaultBranch &&
      inspection.data.branch !== inspection.data.defaultBranch,
  );
  const files = diff.data ? parseUnifiedDiff(diff.data.content) : undefined;
  const stats = files ? diffStats(files) : undefined;
  const summary = changeSummary({
    files: stats?.files,
    additions: stats?.additions,
    deletions: stats?.deletions,
    dirty,
    branchAhead: ahead,
    truncated: diff.data?.truncated,
    pending: diff.isPending,
    unavailable: diff.isError,
  });
  const banner = inspection.data ? checkBanner(capsule.checks, inspection.data.tree) : undefined;
  const canSubmit = Boolean(
    inspection.data &&
      branch.trim() &&
      (!opening || (title.trim() && base.trim() && branch.trim() !== base.trim())) &&
      (!dirty || (opening ? title.trim() : commitMessage.trim())) &&
      !mutation.isPending,
  );

  if (capsule.state !== "Ready") return <p>A pull request needs a Ready Capsule.</p>;
  if (inspection.isPending) return <p role="status">Inspecting Git state…</p>;
  if (inspection.isError) return <p role="alert">{normalizeAPIError(inspection.error).message}</p>;

  return (
    <form className="ship-form pr-form" onSubmit={submit}>
      {banner && (
        <p className={`check-banner check-banner-${capsule.checks?.phase || "idle"}`} role="status">
          {banner}
        </p>
      )}
      <p className="pr-summary" role="status">{summary}</p>
      <label>
        Branch
        <input value={branch} required onChange={(event) => setBranch(event.target.value)} />
      </label>
      {opening && (
        <>
          <label>
            Title
            <input value={title} required onChange={(event) => setTitle(event.target.value)} />
          </label>
          <label>
            Description
            <textarea value={body} rows={4} onChange={(event) => setBody(event.target.value)} />
          </label>
          <label>
            Base branch
            <input value={base} required onChange={(event) => setBase(event.target.value)} />
          </label>
        </>
      )}
      {!opening && dirty && (
        <label>
          Commit message
          <input
            value={commitMessage}
            required
            onChange={(event) => setCommitMessage(event.target.value)}
          />
        </label>
      )}
      {mode === "create" && (
        <label className="check-label">
          <input
            type="checkbox"
            checked={pushOnly}
            onChange={(event) => setPushOnly(event.target.checked)}
          />
          Push branch only
        </label>
      )}
      <details className="pr-details">
        <summary>Reviewed Git state</summary>
        <dl className="facts compact">
          <div><dt>HEAD</dt><dd className="mono">{inspection.data.head}</dd></div>
          <div><dt>Tree</dt><dd className="mono">{inspection.data.tree}</dd></div>
        </dl>
      </details>
      <button type="submit" className="primary-button" disabled={!canSubmit}>
        {mutation.isPending
          ? "Working…"
          : mode === "update"
            ? "Push update"
            : pushOnly
              ? "Push branch"
              : "Create PR"}
      </button>
      {mutation.isError ? <p role="alert">{normalizeAPIError(mutation.error).message}</p> : null}
      {mutation.data ? (
        <p role="status">
          Delivery {mutation.data.id} is {mutation.data.state}.
          {mutation.data.resultPullRequestUrl ? (
            <> <a href={mutation.data.resultPullRequestUrl}>Open pull request</a>.</>
          ) : null}
        </p>
      ) : null}
    </form>
  );
}
