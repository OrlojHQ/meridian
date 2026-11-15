import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import type { HarnessProfileDiagnostic, ProfileDiagnostic } from "../api/generated/types.gen";
import { harnessName } from "../shell/capsuleActivity";

// "Why is this Capsule different from my profile?" (ADR 0049). Every line is
// a fact the control plane holds: the pin, the saved revisions and their
// import reports, the Project's grants. Nothing here claims the Capsule
// matches the laptop, and nothing brings an excluded file back.

function when(value?: string) {
  return value ? new Date(value).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "";
}

function HarnessRow({ row }: { row: HarnessProfileDiagnostic }) {
  const name = harnessName(row.harness);
  const lines: string[] = [];
  if (!row.pinned && row.current) lines.push(`This Capsule has no ${name} setup. New Capsules here use “${row.current.setupName}”.`);
  if (row.pinned && !row.current) {
    lines.push(
      row.projectOverride === "clean"
        ? `This Project now starts ${name} clean. This Capsule keeps “${row.pinned.setupName}”.`
        : `Your profile no longer has a ${name} setup. This Capsule keeps “${row.pinned.setupName}”.`,
    );
  }
  if (row.pinned && row.current && row.pinned.revision !== row.current.revision) {
    lines.push(`Pinned to the import from ${when(row.pinned.savedAt)}. Your profile changed ${when(row.current.savedAt)}.`);
  }
  for (const path of row.filesAdded) lines.push(`Your profile has ${path}; this Capsule does not.`);
  for (const path of row.filesRemoved) lines.push(`This Capsule has ${path}; your profile no longer does.`);
  if (row.connection.granted) {
    if (row.connection.revoked) lines.push(`Login: the connection “${row.connection.name}” was disconnected.`);
    else if (row.connection.needsSignIn) lines.push(`Login: “${row.connection.name}” needs you to sign in again.`);
    else lines.push(`Login: this Project uses the connection “${row.connection.name}”. Your laptop's own sign-in is not copied.`);
  } else {
    lines.push(`Login: this Project has no connection for ${name}, so Threads cannot sign in. Your laptop's sign-in is not copied.`);
  }
  return (
    <li>
      <h3>{name}</h3>
      <ul>
        {lines.map((line) => (
          <li key={line}>{line}</li>
        ))}
        {row.exclusions.map((issue) => (
          <li key={`x-${issue.path}`}>
            Left out <code>{issue.path}</code>: {issue.reason}
          </li>
        ))}
        {row.warnings.map((issue) => (
          <li key={`w-${issue.path}`}>
            Kept <code>{issue.path}</code> with a warning: {issue.reason}
          </li>
        ))}
        {row.pinned && !row.hasReport && <li>This setup was saved before import reports existed, so its exclusions are not known.</li>}
      </ul>
    </li>
  );
}

export function ProfileDiagnosticView({ diagnostic }: { diagnostic: ProfileDiagnostic }) {
  const author = diagnostic.commitAuthor;
  return (
    <>
      {diagnostic.nothingPinned ? (
        <p>This Capsule took nothing from your profile. It was created clean, or before you saved a profile.</p>
      ) : diagnostic.matchesProfile ? (
        <p className="is-ready">This Capsule matches your current profile, with nothing left out.</p>
      ) : (
        <p>This Capsule differs from your current profile:</p>
      )}
      <ul className="profile-diagnostic-harnesses">
        {diagnostic.harnesses.map((row) => (
          <HarnessRow key={row.harness} row={row} />
        ))}
      </ul>
      <p>
        {author.source === "project" && `Delivery commits as the Project's author, ${author.name} <${author.email}>.`}
        {author.source === "capsule" && `Commits are made as ${author.name} <${author.email}>, pinned from your profile.`}
        {author.source === "none" && "No commit identity: set one in your profile or on the Project before Delivery commits."}
        {author.source !== "project" &&
          author.profileName &&
          (author.profileName !== author.name || author.profileEmail !== author.email) &&
          ` Your profile now says ${author.profileName} <${author.profileEmail}>.`}
      </p>
    </>
  );
}

export function CapsuleProfilePanel({ capsuleId }: { capsuleId: string }) {
  const cache = useQueryClient();
  const diagnostic = useQuery({ queryKey: ["capsule-profile", capsuleId], queryFn: () => api.capsuleProfileDiagnostic(capsuleId) });
  const update = useMutation({
    mutationFn: () => api.updateCapsuleProfile(capsuleId),
    onSuccess: (next) => {
      cache.setQueryData(["capsule-profile", capsuleId], next);
      return cache.invalidateQueries({ queryKey: ["capsule", capsuleId] });
    },
  });
  return (
    <div className="panel capsule-profile">
      <h2>Compared with your profile</h2>
      {diagnostic.isPending && <p role="status">Comparing…</p>}
      {diagnostic.isError && <p role="alert">{normalizeAPIError(diagnostic.error).message}</p>}
      {diagnostic.data && <ProfileDiagnosticView diagnostic={diagnostic.data} />}
      {diagnostic.data && !diagnostic.data.matchesProfile && (
        <div className="setup-import-actions">
          <button type="button" className="primary-button" disabled={update.isPending} onClick={() => update.mutate()}>
            Update this Capsule to my current profile
          </button>
          <p className="profile-note">Applies at the next session start. Stop any running session first.</p>
        </div>
      )}
      {update.isError && <p role="alert">{normalizeAPIError(update.error).message}</p>}
    </div>
  );
}
