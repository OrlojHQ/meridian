import { useMutation, useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type {
  Capabilities,
  Capsule,
  PreviewTicket,
  Run,
} from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { DiffViewer } from "../components/DiffViewer";
import { CodeViewer } from "../components/SyntaxCode";
import {
  ActivityIcon,
  ChangesIcon,
  FilesIcon,
  MoreIcon,
  PreviewIcon,
  TerminalIcon,
} from "../components/ui/Icons";

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

function ChangesPane({
  capsule,
  supported,
}: {
  capsule: Capsule;
  supported: boolean;
}) {
  const diff = useQuery({
    ...queries.gitDiff(capsule.id),
    enabled: supported && capsule.state === "Ready",
  });
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
        <span>{diff.data.content ? "Working-tree changes" : "Working tree clean"}</span>
        <Link to={`/ui/capsules/${encodeURIComponent(capsule.id)}/diff`}>
          Full review
        </Link>
      </div>
      <DiffViewer content={diff.data.content} truncated={diff.data.truncated} />
    </div>
  );
}

function decodeWorkspaceText(content: string): string | undefined {
  try {
    const binary = Uint8Array.from(atob(content), (value) => value.charCodeAt(0));
    const text = new TextDecoder("utf-8", { fatal: true }).decode(binary);
    const controls = Array.from(text).filter((value) => {
      const code = value.charCodeAt(0);
      return (
        code === 0 ||
        (code < 32 && value !== "\n" && value !== "\r" && value !== "\t")
      );
    }).length;
    return text.includes("\0") || controls > Math.max(4, text.length / 100)
      ? undefined
      : text;
  } catch {
    return undefined;
  }
}

function FilesPane({
  capsule,
  supported,
}: {
  capsule: Capsule;
  supported: boolean;
}) {
  const [directory, setDirectory] = useState("");
  const [selected, setSelected] = useState("");
  const ready = capsule.state === "Ready";
  const files = useQuery({
    ...queries.workspaceFiles(capsule.id, directory),
    enabled: supported && ready,
  });
  const content = useQuery({
    queryKey: ["workspace-file", capsule.id, selected],
    queryFn: ({ signal }) => api.workspaceFile(capsule.id, selected, signal),
    enabled: supported && ready && Boolean(selected),
  });
  const text = content.data
    ? decodeWorkspaceText(content.data.content)
    : undefined;
  if (!supported) {
    return <p className="tool-empty">Workspace browsing is unsupported.</p>;
  }
  if (!ready) {
    return <p className="tool-empty">Files are available while the Capsule is Ready.</p>;
  }
  if (files.isPending) return <p className="tool-loading" role="status">Loading files…</p>;
  if (files.isError) return <InlineError error={files.error} />;
  const enter = (name: string) => {
    setDirectory(directory ? `${directory}/${name}` : name);
    setSelected("");
  };
  const up = () => {
    setDirectory(directory.split("/").slice(0, -1).join("/"));
    setSelected("");
  };
  return (
    <div className="context-files">
      <div className="context-file-toolbar">
        <button type="button" className="text-button" disabled={!directory} onClick={up}>
          Up
        </button>
        <span className="mono">/{directory}</span>
      </div>
      <div className="context-file-layout">
        <ul aria-label="Workspace file tree">
          {files.data.items.map((entry) => (
            <li key={entry.name}>
              {entry.type === "directory" ? (
                <button
                  type="button"
                  className="file-entry"
                  onClick={() => enter(entry.name)}
                >
                  {entry.name}/
                </button>
              ) : entry.type === "file" ? (
                <button
                  type="button"
                  className="file-entry"
                  disabled={entry.size > 1_048_576}
                  onClick={() =>
                    setSelected(
                      directory ? `${directory}/${entry.name}` : entry.name,
                    )
                  }
                >
                  {entry.name}
                </button>
              ) : (
                <span>{entry.name}</span>
              )}
            </li>
          ))}
        </ul>
        <div className="context-file-viewer" aria-live="polite">
          {!selected ? (
            <p className="tool-empty">Select a text file.</p>
          ) : content.isPending ? (
            <p className="tool-loading" role="status">Loading file…</p>
          ) : content.isError ? (
            <p className="inline-error" role="alert">File unavailable or too large.</p>
          ) : text === undefined ? (
            <p className="tool-empty">Binary content is unsupported.</p>
          ) : (
            <CodeViewer content={text} path={selected} />
          )}
        </div>
      </div>
    </div>
  );
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

  return (
    <aside className="context-panel" aria-label="Capsule tools">
      <div className="context-tabs" role="tablist" aria-label="Capsule tools">
        {tabs.map(({ id, label, icon: TabIcon }) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={tab === id}
            aria-controls={`context-${id}`}
            title={label}
            onClick={() => setTab(id)}
          >
            <TabIcon />
            <span>{label}</span>
          </button>
        ))}
      </div>
      <section
        id={`context-${tab}`}
        className="context-tool"
        role="tabpanel"
        aria-label={tabs.find((item) => item.id === tab)?.label}
      >
        {tab === "changes" && (
          <ChangesPane capsule={capsule.data} supported={capabilities?.git === true} />
        )}
        {tab === "preview" && (
          <PreviewPane capsule={capsule.data} supported={capabilities?.preview === true} />
        )}
        {tab === "files" && (
          <FilesPane capsule={capsule.data} supported={capabilities?.browse === true} />
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
