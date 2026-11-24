import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import { queries } from "../api/queries";

// GitHubApp is the Station's GitHub App (ADR 0061). Creating it posts a
// manifest to GitHub. The private key comes back to the Station and is never
// shown here.
export function GitHubApp() {
  const cache = useQueryClient();
  const app = useQuery(queries.githubApp());
  const [organization, setOrganization] = useState("");
  const sync = useMutation({
    mutationFn: () => api.syncGitHubAppInstallations(),
    onSuccess: (next) => cache.setQueryData(queries.githubApp().queryKey, next),
  });
  const setup = useMutation({
    mutationFn: () => api.beginGitHubAppSetup(organization.trim()),
    onSuccess: (result) => {
      // Same-origin handoff. The page there posts the manifest. A cross-origin
      // form from this page is blocked by the Station content security policy,
      // and the desktop shell opens the handoff in the system browser.
      if (!result.handoffUrl.startsWith("/") || result.handoffUrl.startsWith("//")) return;
      window.location.assign(result.handoffUrl);
    },
  });
  const remove = useMutation({
    mutationFn: () => api.deleteGitHubApp(app.data?.resourceVersion ?? 0),
    onSuccess: () => cache.invalidateQueries({ queryKey: queries.githubApp().queryKey }),
  });

  if (app.isPending) return <p>Loading the GitHub App…</p>;
  if (app.isError) return <p>{normalizeAPIError(app.error).message}</p>;
  const value = app.data;

  return (
    <section id="github-app" aria-labelledby="github-app-title">
      <h3 id="github-app-title">GitHub App</h3>
      <p>
        Capsules use a short-lived token for one repository, minted by a GitHub App that stays on this Station. The
        App's private key never enters a Capsule. Your GitHub login under Account still handles clone, push, and pull
        requests on the Station.
      </p>
      {!value.configured ? (
        <>
          <p className="is-warning" role="status">
            This Station has no GitHub App, so Capsules cannot use <code>gh</code>.
            {value.canManage
              ? " Create one, then install it and choose All repositories."
              : " Ask an administrator to create one."}
          </p>
          {value.canManage && (
            <form
              className="profile-git"
              onSubmit={(event) => {
                event.preventDefault();
                setup.mutate();
              }}
            >
              <label>
                Organization
                <input
                  value={organization}
                  maxLength={39}
                  placeholder="Leave empty for your user account"
                  onChange={(event) => setOrganization(event.target.value)}
                />
              </label>
              <button type="submit" className="primary-button" disabled={setup.isPending}>
                Create GitHub App
              </button>
            </form>
          )}
          {setup.isError && <p role="alert">{normalizeAPIError(setup.error).message}</p>}
        </>
      ) : (
        <>
          <p>
            <a href={value.htmlUrl}>{value.name ?? value.slug}</a>
            {value.ownerLogin ? ` on ${value.ownerLogin}` : ""}.{" "}
            {value.webhook
              ? "GitHub can notify this Station when checks change."
              : "This Station has no public HTTPS address, so it keeps polling checks."}
          </p>
          {value.installations.length === 0 ? (
            <p className="is-warning" role="status">
              The App is saved here and is not installed yet. Install it on your account or organization and choose All
              repositories so private repositories work without another setup step.
            </p>
          ) : (
            <ul>
              {value.installations.map((item) => (
                <li key={item.id}>
                  {item.accountLogin} ({item.accountType.toLowerCase()}
                  {item.repositorySelection === "all" ? ", all repositories" : ", selected repositories"}
                  {item.suspended ? ", suspended" : ""})
                </li>
              ))}
            </ul>
          )}
          <p>
            {value.installUrl && (
              <a className="button-link" href={value.installUrl}>
                Install on GitHub
              </a>
            )}
            {value.canManage && (
              <>
                {" "}
                <button type="button" disabled={sync.isPending} onClick={() => sync.mutate()}>
                  Refresh installations
                </button>{" "}
                <button type="button" className="danger" disabled={remove.isPending} onClick={() => remove.mutate()}>
                  Remove from this Station
                </button>
              </>
            )}
          </p>
          {sync.isError && <p role="alert">{normalizeAPIError(sync.error).message}</p>}
          {remove.isError && <p role="alert">{normalizeAPIError(remove.error).message}</p>}
        </>
      )}
    </section>
  );
}

// GitHubAppNotice tells the Project page when Capsules cannot use gh.
export function GitHubAppNotice({ projectId }: { projectId: string }) {
  const access = useQuery(queries.githubAppAccess(projectId));
  if (!access.data || access.data.state === "ready" || access.data.state === "not_github" || !access.data.message) {
    return null;
  }
  return (
    <p className="is-warning" role="status">
      {access.data.message}{" "}
      {access.data.state === "off" ? (
        <Link to="/ui/?settings=general">Settings, General</Link>
      ) : (
        <Link to="/ui/settings/account#github-app">GitHub App settings</Link>
      )}
      {access.data.installUrl && access.data.state === "not_installed" ? (
        <>
          {" "}
          <a href={access.data.installUrl}>Install on GitHub</a>
        </>
      ) : null}
    </p>
  );
}
