import { useMutation, useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type {
  Capabilities,
  Capsule,
  PreviewTicket,
  Run,
} from "../api/generated/types.gen";
import { queries } from "../api/queries";
import {
  type DiffStats,
  diffStats,
  DiffViewer,
  parseUnifiedDiff,
} from "../components/DiffViewer";
import { ReviewBatch } from "../components/ReviewComments";
import { useReviewComments } from "../components/reviewBatch";
import {
  ActivityIcon,
  ChangesIcon,
  FilesIcon,
  MoreIcon,
  PreviewIcon,
  TerminalIcon,
} from "../components/ui/Icons";
import { WorkspaceFiles } from "../components/WorkspaceFiles";

const TerminalView = lazy(() =>
  import("../components/Terminal").then((module) => ({
    default: module.TerminalView,
  })),
);

type ContextTab = "changes" | "preview" | "files" | "terminal" | "activity";

const tabFromPath = (pathname: string): ContextTab => {
  if (pathname.endsWith("/diff")) return "changes";
  if (pathname.endsWith("#terminal")) return "terminal";
  if (
    pathname.endsWith("/terminal") ||
    pathname.includes("/runs/") ||
    pathname.includes("/moments/") ||
    pathname.includes("/timelines/")
  ) {
    return "activity";
  }
  return "changes";
};

function InlineError({ error }: { error: unknown }) {
  return (
    <p className="inline-error" role="alert">
      {normalizeAPIError(error).message}
    </p>
  );
}

// Parses the Capsule's diff query. The tab label passes `fetch: false` so it
// only reads a diff the Changes tool already loaded: Git access counts as
// Capsule activity and must never happen in the background.
function useLoadedDiff(capsuleId: string, fetch: boolean) {
  const diff = useQuery({ ...queries.gitDiff(capsuleId), enabled: fetch });
  const content = diff.data?.content;
  const files = useMemo(
    () => (content === undefined ? undefined : parseUnifiedDiff(content)),
    [content],
  );
  return { diff, files, stats: files ? diffStats(files) : undefined };
}

const compactCount = (value: number) =>
  value < 1_000
    ? String(value)
    : `${(value / 1_000).toFixed(value < 10_000 ? 1 : 0)}k`;

const plural = (count: number, noun: string) =>
  `${count} ${noun}${count === 1 ? "" : "s"}`;

function describeStats(stats: DiffStats, truncated: boolean) {
  return `${plural(stats.files, "file")} changed, ${plural(
    stats.additions,
    "addition",
  )}, ${plural(stats.deletions, "deletion")}${truncated ? " (diff truncated)" : ""}`;
}

function LineCounts({ stats }: { stats: DiffStats }) {
  return (
    <span className="diff-file-stats" aria-hidden="true">
      <i className="additions">+{compactCount(stats.additions)}</i>
      <i className="deletions">−{compactCount(stats.deletions)}</i>
    </span>
  );
}

function ChangesPane({
  capsule,
  supported,
  structured,
}: {
  capsule: Capsule;
  supported: boolean;
  structured: boolean;
}) {
  const { diff, files = [], stats } = useLoadedDiff(
    capsule.id,
    supported && capsule.state === "Ready",
  );
  const review = useReviewComments(capsule.id);
  if (!supported) {
    return <p className="tool-empty">Git review is unsupported by this provider.</p>;
  }
  if (capsule.state !== "Ready") {
    return <p className="tool-empty">Changes are available while the Capsule is Ready.</p>;
  }
  if (diff.isPending) return <p className="tool-loading" role="status">Loading changes…</p>;
  if (diff.isError) return <InlineError error={diff.error} />;
  return (
    <div className="tool-pane-content">
      <div className="tool-pane-actions">
        {diff.data.content && stats ? (
          <span className="diff-summary">
            <strong>
              {plural(stats.files, "file")}
              {diff.data.truncated ? "+" : ""} changed
            </strong>
            <LineCounts stats={stats} />
            <span className="sr-only">
              {describeStats(stats, diff.data.truncated)}
            </span>
          </span>
        ) : (
          <span>Working tree clean</span>
        )}
        <Link to={`/ui/capsules/${encodeURIComponent(capsule.id)}/diff`}>
          Full review
        </Link>
      </div>
      <ReviewBatch capsule={capsule} files={files} structured={structured} />
      <DiffViewer
        content={diff.data.content}
        truncated={diff.data.truncated}
        review={review}
      />
    </div>
  );
}

function FilesPane({
  capsule,
  supported,
}: {
  capsule: Capsule;
  supported: boolean;
}) {
  if (!supported) {
    return <p className="tool-empty">Workspace browsing is unsupported.</p>;
  }
  if (capsule.state !== "Ready") {
    return <p className="tool-empty">Files are available while the Capsule is Ready.</p>;
  }
  return <WorkspaceFiles capsuleId={capsule.id} />;
}

function PreviewPane({
  capsule,
  supported,
}: {
  capsule: Capsule;
  supported: boolean;
}) {
  const ports = useQuery({
    ...queries.previewPorts(capsule.id),
    enabled: supported && capsule.state === "Ready",
  });
  const [ticket, setTicket] = useState<PreviewTicket & { port: number }>();
  const issue = useMutation({
    mutationFn: (port: number) => api.previewTicket(capsule.id, port),
    onSuccess: (value, port) => setTicket({ port, ...value }),
  });
  if (!supported) {
    return <p className="tool-empty">Previews are unsupported by this provider.</p>;
  }
  if (capsule.state !== "Ready") {
    return <p className="tool-empty">Preview links are revoked while this Capsule is not Ready.</p>;
  }
  if (ports.isPending) return <p className="tool-loading" role="status">Discovering ports…</p>;
  if (ports.isError) return <InlineError error={ports.error} />;
  return (
    <div className="tool-pane-content">
      <p className="tool-description">
        Preview applications open through short-lived Capsule-scoped links.
      </p>
      {ports.data.items.length === 0 ? (
        <p className="tool-empty">No reachable HTTP ports.</p>
      ) : (
        <ul className="context-resource-list" aria-label="Discovered preview ports">
          {ports.data.items.map(({ port }) => (
            <li key={port}>
              <span>
                <strong>Port {port}</strong>
                <small>HTTP preview</small>
              </span>
              <button
                type="button"
                className="text-button"
                disabled={issue.isPending}
                onClick={() => issue.mutate(port)}
              >
                Open
              </button>
            </li>
          ))}
        </ul>
      )}
      {issue.isError && <InlineError error={issue.error} />}
      {ticket && Date.parse(ticket.expiresAt) > Date.now() && (
        <a
          className="preview-launch"
          href={ticket.url}
          target="_blank"
          rel="noreferrer"
        >
          Open port {ticket.port} in a new tab
        </a>
      )}
    </div>
  );
}

function TerminalPane({
  capsule,
  runs,
  supported,
  focused,
}: {
  capsule: Capsule;
  runs: Run[];
  supported: boolean;
  focused: boolean;
}) {
  const run =
    runs.find((item) =>
      ["Queued", "Starting", "Running", "Cancelling"].includes(item.state),
    ) ?? runs[0];
  if (!supported) {
    return <p className="tool-empty">Terminal attachment is unsupported.</p>;
  }
  if (!run) {
    return <p className="tool-empty">No Run terminal exists for this Capsule.</p>;
  }
  if (focused) {
    return (
      <div className="tool-empty">
        Terminal is focused in the main workspace.
        <Link to={`/ui/runs/${encodeURIComponent(run.id)}`}>Inspect Run</Link>
      </div>
    );
  }
  const reconnect = ["Queued", "Starting", "Running", "Cancelling"].includes(
    run.state,
  );
  return (
    <div className="context-terminal">
      <div className="tool-pane-actions">
        <span>{run.harness} · {run.state}</span>
        <Link to={`/ui/runs/${encodeURIComponent(run.id)}/terminal`}>Focus</Link>
      </div>
      <Suspense fallback={<p className="tool-loading">Loading terminal…</p>}>
        <TerminalView
          runId={run.id}
          reconnect={reconnect}
          supported={supported}
        />
      </Suspense>
    </div>
  );
}

function ActivityPane({
  capsule,
  runs,
  snapshotSupported,
}: {
  capsule: Capsule;
  runs: Run[];
  snapshotSupported: boolean;
}) {
  const moments = useQuery({
    ...queries.moments(capsule.id),
    enabled: snapshotSupported,
  });
  return (
    <div className="tool-pane-content">
      <dl className="context-facts">
        <div><dt>State</dt><dd>{capsule.state}</dd></div>
        <div><dt>Harness</dt><dd>{capsule.harness || "Structured"}</dd></div>
        <div><dt>Timeline</dt><dd><Link to={`/ui/timelines/${capsule.timelineId}`}>View</Link></dd></div>
        <div><dt>Updated</dt><dd>{new Date(capsule.updatedAt).toLocaleString()}</dd></div>
      </dl>
      <h3>Runs</h3>
      {runs.length === 0 ? (
        <p className="tool-empty">No Runs yet.</p>
      ) : (
        <ul className="context-resource-list">
          {runs.map((run) => (
            <li key={run.id}>
              <Link to={`/ui/runs/${encodeURIComponent(run.id)}`}>
                <strong>{run.harness}</strong>
                <small>{run.state}</small>
              </Link>
              <time dateTime={run.createdAt}>
                {new Date(run.createdAt).toLocaleTimeString()}
              </time>
            </li>
          ))}
        </ul>
      )}
      <h3>Moments</h3>
      {!snapshotSupported ? (
        <p className="tool-empty">Filesystem Moments are unsupported by this provider.</p>
      ) : moments.isPending ? (
        <p className="tool-loading" role="status">Loading Moments…</p>
      ) : moments.isError ? (
        <InlineError error={moments.error} />
      ) : moments.data.items.length === 0 ? (
        <p className="tool-empty">No Moments captured yet.</p>
      ) : (
        <ul className="context-resource-list">
          {moments.data.items.map((moment) => (
            <li key={moment.id}>
              <Link to={`/ui/moments/${encodeURIComponent(moment.id)}`}>
                <strong>{moment.name}</strong>
                <small>{moment.final ? "Final" : "Checkpoint"}</small>
              </Link>
              <time dateTime={moment.createdAt}>
                {new Date(moment.createdAt).toLocaleTimeString()}
              </time>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function WorkspaceContext({
  capsuleId,
  pathname,
  session = "",
  capabilities,
}: {
  capsuleId: string;
  pathname: string;
  session?: string;
  capabilities?: Capabilities;
}) {
  const [tab, setTab] = useState<ContextTab>(() => tabFromPath(pathname));
  const capsule = useQuery(queries.capsule(capsuleId));
  const runs = useQuery(queries.runs(capsuleId));
  const loadedDiff = useLoadedDiff(capsuleId, false);
  useEffect(() => setTab(tabFromPath(pathname)), [pathname]);

  if (capsule.isPending || runs.isPending) {
    return <aside className="context-panel"><p className="tool-loading">Loading workspace…</p></aside>;
  }
  if (capsule.isError) {
    return <aside className="context-panel"><InlineError error={capsule.error} /></aside>;
  }
  if (runs.isError) {
    return <aside className="context-panel"><InlineError error={runs.error} /></aside>;
  }

  const allTabs: Array<{
    id: ContextTab;
    label: string;
    icon: typeof MoreIcon;
  }> = [
    { id: "changes", label: "Changes", icon: ChangesIcon },
    { id: "preview", label: "Preview", icon: PreviewIcon },
    { id: "files", label: "Files", icon: FilesIcon },
    { id: "terminal", label: "Terminal", icon: TerminalIcon },
    { id: "activity", label: "Activity", icon: ActivityIcon },
  ];
  const tabs = allTabs.filter(
    ({ id }) =>
      id !== "terminal" ||
      !(
        pathname.endsWith("/terminal") ||
        (Boolean(capsule.data.harness) &&
          /^\/ui\/capsules\/[^/]+\/?$/.test(pathname) &&
          (session === "" || session === "terminal"))
      ),
  );

  const changes =
    capabilities?.git === true &&
    capsule.data.state === "Ready" &&
    loadedDiff.diff.data?.content &&
    loadedDiff.stats?.files
      ? {
          stats: loadedDiff.stats,
          truncated: loadedDiff.diff.data.truncated,
        }
      : undefined;

  return (
    <aside className="context-panel" aria-label="Capsule tools">
      <div className="context-tabs" role="tablist" aria-label="Capsule tools">
        {tabs.map(({ id, label, icon: TabIcon }) => {
          const counts = id === "changes" ? changes : undefined;
          const name = counts
            ? `${label}, ${describeStats(counts.stats, counts.truncated)}`
            : label;
          return (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={tab === id}
              aria-controls={`context-${id}`}
              aria-label={name}
              title={name}
              onClick={() => setTab(id)}
            >
              <TabIcon />
              <span>{label}</span>
              {counts && (
                <small className="tab-stats" aria-hidden="true">
                  <b>
                    {compactCount(counts.stats.files)}
                    {counts.truncated ? "+" : ""}
                  </b>
                  <LineCounts stats={counts.stats} />
                </small>
              )}
            </button>
          );
        })}
      </div>
      <section
        id={`context-${tab}`}
        className="context-tool"
        role="tabpanel"
        aria-label={tabs.find((item) => item.id === tab)?.label}
      >
        {tab === "changes" && (
          <ChangesPane
            capsule={capsule.data}
            supported={capabilities?.git === true}
            structured={capabilities?.structured === true || !capsule.data.harness}
          />
        )}
        {tab === "preview" && (
          <PreviewPane capsule={capsule.data} supported={capabilities?.preview === true} />
        )}
        {tab === "files" && (
          <FilesPane
            key={capsule.data.id}
            capsule={capsule.data}
            supported={capabilities?.browse === true}
          />
        )}
        {tab === "terminal" && (
          <TerminalPane
            capsule={capsule.data}
            runs={runs.data.items}
            supported={capabilities?.attach === true}
            focused={pathname.endsWith("/terminal")}
          />
        )}
        {tab === "activity" && (
          <ActivityPane
            capsule={capsule.data}
            runs={runs.data.items}
            snapshotSupported={capabilities?.snapshot === true}
          />
        )}
      </section>
    </aside>
  );
}
