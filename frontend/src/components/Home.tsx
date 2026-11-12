import { useMemo } from "react";
import { Link, useOutletContext } from "react-router-dom";

import { relativeTime, sessionPath } from "../shell/capsuleActivity";
import type { ShellOutletContext } from "../shell/AppShell";
import { useWorkingSet } from "../shell/useWorkingSet";
import { PlusIcon, TerminalIcon, ThreadIcon } from "./ui/Icons";

// Home lists recent work. Starting a Capsule happens in the New Capsule
// dialog, which needs only a mode, a Project, and a harness.
export function Home() {
  const outlet = useOutletContext<ShellOutletContext | undefined>();
  const { projectItems, items } = useWorkingSet();

  const recent = useMemo(
    () =>
      items
        .filter((item) => item.activity.group !== "attention")
        .sort(
          (left, right) =>
            Date.parse(right.activity.since ?? right.capsule.updatedAt) -
            Date.parse(left.activity.since ?? left.capsule.updatedAt),
        )
        .slice(0, 5),
    [items],
  );

  if (projectItems.length === 0) {
    return (
      <section className="home empty-state">
        <h1>No projects yet</h1>
        <p>A Project points Meridian at a repository. Every session runs in its own Capsule.</p>
        {outlet?.openNewProject && (
          <button type="button" className="primary-button" onClick={outlet.openNewProject}>
            New project
          </button>
        )}
      </section>
    );
  }

  return (
    <section className="home">
      <div className="home-start">
        <h1>Start something new</h1>
        <p>Each session runs in its own Capsule.</p>
        {outlet?.openNewSession && (
          <button type="button" className="primary-button" onClick={() => outlet.openNewSession()}>
            <PlusIcon />
            New Capsule
          </button>
        )}
      </div>
      {recent.length > 0 && (
        <section className="home-recent" aria-label="Recent Capsules">
          <h2>Recent</h2>
          <ul>
            {recent.map((item) => (
              <li key={item.capsule.id}>
                <Link to={sessionPath(item.capsule.id)}>
                  {item.sessions.some((session) => session.kind === "thread" && !session.archived) ? (
                    <ThreadIcon />
                  ) : (
                    <TerminalIcon />
                  )}
                  <strong>{item.capsule.name}</strong>
                  <em>{item.activity.label}</em>
                  <time dateTime={item.activity.since}>{relativeTime(item.activity.since)}</time>
                </Link>
              </li>
            ))}
          </ul>
        </section>
      )}
    </section>
  );
}
