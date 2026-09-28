import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { type KeyboardEvent, useMemo, useRef, useState } from "react";

import { normalizeAPIError } from "../api/client";
import type { WorkspaceFileEntry } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { CodeViewer } from "./SyntaxCode";
import { ChevronIcon } from "./ui/Icons";

export const MAX_WORKSPACE_FILE_BYTES = 1_048_576;

export function decodeWorkspaceText(content: string): string | undefined {
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

type TreeRow = {
  path: string;
  parent: string;
  entry: WorkspaceFileEntry;
  level: number;
  position: number;
  setSize: number;
};

const joinPath = (parent: string, name: string) =>
  parent ? `${parent}/${name}` : name;

const isWithin = (path: string, directory: string) =>
  path.startsWith(`${directory}/`);

const openable = (entry: WorkspaceFileEntry) =>
  entry.type === "file" && entry.size <= MAX_WORKSPACE_FILE_BYTES;

// Directories first, keeping the server's order within each group.
const ordered = (items: WorkspaceFileEntry[]) => [
  ...items.filter((entry) => entry.type === "directory"),
  ...items.filter((entry) => entry.type !== "directory"),
];

// A lazily loaded tree of the Capsule workspace. Reading a directory counts as
// Capsule activity, so a directory is listed only after the operator expands
// it, and listings refresh only when the operator asks.
export function WorkspaceFileTree({
  capsuleId,
  selected,
  onOpen,
}: {
  capsuleId: string;
  selected: string;
  onOpen: (path: string) => void;
}) {
  const [expanded, setExpanded] = useState<string[]>([]);
  const [active, setActive] = useState("");
  const items = useRef(new Map<string, HTMLLIElement>());
  const directories = useMemo(() => ["", ...expanded], [expanded]);
  const listings = useQueries({
    queries: directories.map((path) => queries.workspaceFiles(capsuleId, path)),
  });
  const listing = (path: string) => listings[directories.indexOf(path)];

  const rows: TreeRow[] = [];
  const walk = (directory: string, level: number) => {
    const page = listing(directory)?.data;
    if (!page) return;
    const entries = ordered(page.items);
    entries.forEach((entry, index) => {
      const path = joinPath(directory, entry.name);
      rows.push({
        path,
        parent: directory,
        entry,
        level,
        position: index + 1,
        setSize: entries.length,
      });
      if (entry.type === "directory" && expanded.includes(path)) {
        walk(path, level + 1);
      }
    });
  };
  walk("", 1);

  const root = listings[0];
  if (root.isPending) {
    return <p className="tool-loading" role="status">Loading files…</p>;
  }
  if (root.isError) {
    return (
      <p className="inline-error" role="alert">
        {normalizeAPIError(root.error).message}
      </p>
    );
  }
  if (rows.length === 0) {
    return <p className="tool-empty">This workspace is empty.</p>;
  }

  const current = rows.find((row) => row.path === active) ?? rows[0];
  const focus = (path: string) => {
    setActive(path);
    items.current.get(path)?.focus();
  };
  const expand = (path: string) =>
    setExpanded((value) => (value.includes(path) ? value : [...value, path]));
  const collapse = (path: string) => {
    // Descendants collapse too, so hidden listings are never refreshed.
    setExpanded((value) =>
      value.filter((item) => item !== path && !isWithin(item, path)),
    );
    if (isWithin(active, path)) setActive(path);
  };
  const activate = (row: TreeRow) => {
    if (row.entry.type === "directory") {
      if (expanded.includes(row.path)) collapse(row.path);
      else expand(row.path);
    } else if (openable(row.entry)) {
      onOpen(row.path);
    }
  };

  const onKeyDown = (event: KeyboardEvent<HTMLUListElement>) => {
    const index = rows.indexOf(current);
    const directory = current.entry.type === "directory";
    const open = directory && expanded.includes(current.path);
    switch (event.key) {
      case "ArrowDown":
        if (rows[index + 1]) focus(rows[index + 1].path);
        break;
      case "ArrowUp":
        if (index > 0) focus(rows[index - 1].path);
        break;
      case "Home":
        focus(rows[0].path);
        break;
      case "End":
        focus(rows[rows.length - 1].path);
        break;
      case "ArrowRight":
        if (directory && !open) expand(current.path);
        else if (open && rows[index + 1]?.parent === current.path) {
          focus(rows[index + 1].path);
        }
        break;
      case "ArrowLeft":
        if (open) collapse(current.path);
        else if (current.parent) focus(current.parent);
        break;
      case "Enter":
      case " ":
        activate(current);
        break;
      default:
        return;
    }
    event.preventDefault();
  };

  return (
    <ul
      className="file-tree-list"
      role="tree"
      aria-label="Workspace files"
      onKeyDown={onKeyDown}
    >
      {rows.map((row) => {
        const { entry, path } = row;
        const directory = entry.type === "directory";
        const open = directory && expanded.includes(path);
        const children = open ? listing(path) : undefined;
        const note = directory
          ? children?.isPending
            ? "Loading…"
            : children?.isError
              ? "Unavailable"
              : children?.data?.items.length === 0
                ? "Empty"
                : children?.data?.nextAfter
                  ? `First ${children.data.items.length} shown`
                  : undefined
          : entry.type === "file"
            ? entry.size > MAX_WORKSPACE_FILE_BYTES
              ? "Too large"
              : undefined
            : `${entry.type === "symlink" ? "Symlink" : "Special file"}, unsupported`;
        return (
          <li
            key={path}
            ref={(element) => {
              if (element) items.current.set(path, element);
              else items.current.delete(path);
            }}
            role="treeitem"
            className={`file-tree-item${directory ? " directory" : ""}`}
            tabIndex={path === current.path ? 0 : -1}
            title={path}
            aria-level={row.level}
            aria-posinset={row.position}
            aria-setsize={row.setSize}
            aria-expanded={directory ? open : undefined}
            aria-selected={path === selected}
            aria-disabled={!directory && !openable(entry) ? true : undefined}
            aria-busy={children?.isPending ? true : undefined}
            onFocus={() => setActive(path)}
            onClick={() => {
              setActive(path);
              activate(row);
            }}
          >
            {Array.from({ length: row.level - 1 }, (_, index) => (
              <span key={index} className="file-tree-indent" aria-hidden="true" />
            ))}
            <span className="file-tree-toggle" aria-hidden="true">
              {directory && <ChevronIcon />}
            </span>
            <span className="file-tree-name">{entry.name}</span>
            {note && <small>{note}</small>}
          </li>
        );
      })}
    </ul>
  );
}

export function WorkspaceFiles({ capsuleId }: { capsuleId: string }) {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState("");
  const content = useQuery(queries.workspaceFile(capsuleId, selected));
  const text = content.data
    ? decodeWorkspaceText(content.data.content)
    : undefined;
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["workspace-files", capsuleId] }),
      queryClient.invalidateQueries({ queryKey: ["workspace-file", capsuleId] }),
    ]);
  return (
    <div className="context-files">
      <div className="context-file-toolbar">
        <span className="mono">/{selected}</span>
        <button type="button" className="text-button" onClick={() => void refresh()}>
          Refresh
        </button>
      </div>
      <div className="context-file-layout">
        <div className="context-file-tree">
          <WorkspaceFileTree
            capsuleId={capsuleId}
            selected={selected}
            onOpen={setSelected}
          />
        </div>
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
