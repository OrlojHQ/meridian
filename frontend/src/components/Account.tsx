import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import { queries } from "../api/queries";

// Account shows who is signed in, their sessions, and, before the
// installation is claimed, how to turn on team mode (ADR 0046).
export function Account() {
  const mode = useQuery(queries.authenticationMode());
  const [params] = useSearchParams();
  if (mode.isPending) return <p className="muted">Loading account…</p>;
  if (mode.isError) {
    return (
      <section className="error-state" role="alert">
        <h2>Account unavailable</h2>
        <p>{normalizeAPIError(mode.error).message}</p>
      </section>
    );
  }
  return (
    <section className="settings-page" aria-labelledby="account-title">
      <h1 id="account-title">Account</h1>
      {mode.data.mode === "team" ? (
        <TeamAccount />
      ) : params.get("claim") === "ready" ? (
        <ClaimInstallation />
      ) : (
        <SingleUserAccount claimable={mode.data.claimable} />
      )}
    </section>
  );
}

function SingleUserAccount({ claimable }: { claimable: boolean }) {
  return (
    <>
      <p>
        This installation is in single-user mode. The installation token and
        this browser's session act as its owner.
      </p>
      {claimable ? (
        <section aria-labelledby="team-mode-title">
          <h2 id="team-mode-title">Team mode</h2>
          <p>
            Team mode signs each teammate in through your identity provider
            and keeps everyone's work private to them. To turn it on, sign in
            as the owner, then confirm with the installation token. This
            happens once and cannot be undone.
          </p>
          <a className="button" href="/auth/oidc/start">
            Sign in to claim this installation
          </a>
        </section>
      ) : null}
    </>
  );
}

function ClaimInstallation() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setSubmitting(true);
    const form = event.currentTarget;
    const token = new FormData(form).get("token");
    try {
      if (typeof token !== "string" || token === "") {
        throw new Error("Enter the installation API token");
      }
      await api.claimInstallation(token);
      form.reset();
      window.location.assign("/ui/settings/account");
    } catch (value) {
      form.reset();
      setError(normalizeAPIError(value).message);
      setSubmitting(false);
    }
  }
  return (
    <section aria-labelledby="claim-title">
      <h2 id="claim-title">Claim this installation</h2>
      <p>
        You are signed in with your identity provider. Enter the installation
        token to bind that identity to this installation's owner and turn on
        team mode. Every existing Project, secret, and connection stays yours.
      </p>
      <form onSubmit={submit}>
        <label htmlFor="claim-token">Installation token</label>
        <input id="claim-token" name="token" type="password" autoComplete="off" required />
        <button type="submit" disabled={submitting}>
          {submitting ? "Claiming…" : "Claim and turn on team mode"}
        </button>
      </form>
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}

function TeamAccount() {
  const member = useQuery(queries.currentMember());
  const sessions = useQuery(queries.sessions());
  const client = useQueryClient();
  const revoke = useMutation({
    mutationFn: (sessionId: string) => api.revokeSession(sessionId),
    onSuccess: () => client.invalidateQueries({ queryKey: queries.sessions().queryKey }),
  });
  const signOut = useMutation({
    mutationFn: () => api.signOut(),
    onSuccess: () => window.location.assign("/ui/"),
  });
  return (
    <>
      {member.data ? (
        <dl>
          <dt>Signed in as</dt>
          <dd>{member.data.displayName || member.data.email || member.data.id}</dd>
          {member.data.email ? (
            <>
              <dt>Email</dt>
              <dd>{member.data.email}</dd>
            </>
          ) : null}
          <dt>Role</dt>
          <dd>{member.data.role}</dd>
        </dl>
      ) : null}
      <button type="button" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
        Sign out
      </button>
      <PersonalAccessTokens />
      {member.data?.role === "admin" ? <Members selfId={member.data.id} /> : null}
      <section aria-labelledby="sessions-title">
        <h2 id="sessions-title">Sessions</h2>
        <p className="muted">Sessions end after 12 hours idle or 7 days.</p>
        <ul>
          {(sessions.data?.items ?? []).map((session) => (
            <li key={session.id}>
              <span>
                Signed in {new Date(session.createdAt).toLocaleString()}
                {session.current ? " (this browser)" : ""}
                {session.revoked ? " · revoked" : ""}
              </span>
              {!session.revoked && !session.current ? (
                <button
                  type="button"
                  onClick={() => revoke.mutate(session.id)}
                  disabled={revoke.isPending}
                >
                  Revoke
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      </section>
    </>
  );
}

// PersonalAccessTokens mints tokens for the CLI and TUI. A new token's value
// is shown once, held only in component state, and never written to
// browser storage.
function PersonalAccessTokens() {
  const tokens = useQuery(queries.personalAccessTokens());
  const client = useQueryClient();
  const [minted, setMinted] = useState("");
  const [error, setError] = useState("");
  const refresh = () => client.invalidateQueries({ queryKey: queries.personalAccessTokens().queryKey });
  const mint = useMutation({
    mutationFn: (body: { name: string; readOnly: boolean; expiresInDays: number }) =>
      api.mintPersonalAccessToken(body),
    onSuccess: (token) => {
      setMinted(token.token);
      setError("");
      void refresh();
    },
    onError: (value) => setError(normalizeAPIError(value).message),
  });
  const revoke = useMutation({
    mutationFn: (tokenId: string) => api.revokePersonalAccessToken(tokenId),
    onSuccess: () => void refresh(),
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    mint.mutate({
      name: String(form.get("name") ?? ""),
      readOnly: form.get("readOnly") === "on",
      expiresInDays: Number(form.get("expiresInDays") ?? 90),
    });
    event.currentTarget.reset();
  }
  return (
    <section aria-labelledby="tokens-title">
      <h2 id="tokens-title">Personal access tokens</h2>
      <p className="muted">
        Tokens let the CLI and TUI act as you. Save one to a file and pass it
        with <code>--token-file</code> or <code>MERIDIAN_TOKEN_FILE</code>.
      </p>
      <form onSubmit={submit}>
        <label htmlFor="token-name">Name</label>
        <input id="token-name" name="name" required maxLength={64} />
        <label htmlFor="token-expiry">Expires after</label>
        <select id="token-expiry" name="expiresInDays" defaultValue="90">
          <option value="30">30 days</option>
          <option value="90">90 days</option>
          <option value="365">1 year</option>
        </select>
        <label>
          <input type="checkbox" name="readOnly" /> Read-only
        </label>
        <button type="submit" disabled={mint.isPending}>
          Create token
        </button>
      </form>
      {error ? <p role="alert">{error}</p> : null}
      {minted ? (
        <div role="status">
          <p>Copy this token now. It will not be shown again.</p>
          <code aria-label="New token">{minted}</code>
          <button type="button" onClick={() => setMinted("")}>
            Done
          </button>
        </div>
      ) : null}
      <ul>
        {(tokens.data?.items ?? []).map((token) => (
          <li key={token.id}>
            <span>
              {token.name}
              {token.readOnly ? " · read-only" : ""} · expires{" "}
              {new Date(token.expiresAt).toLocaleDateString()}
              {token.revoked ? " · revoked" : ""}
            </span>
            {!token.revoked ? (
              <button
                type="button"
                aria-label={`Revoke ${token.name}`}
                onClick={() => revoke.mutate(token.id)}
                disabled={revoke.isPending}
              >
                Revoke
              </button>
            ) : null}
          </li>
        ))}
      </ul>
    </section>
  );
}

// Members is membership administration for admins. Admins disable members,
// change roles, and revoke credentials; they never see a member's work.
function Members({ selfId }: { selfId: string }) {
  const members = useQuery(queries.members());
  const client = useQueryClient();
  const [error, setError] = useState("");
  const change = useMutation({
    mutationFn: (run: () => Promise<void>) => run(),
    onSuccess: () => {
      setError("");
      void client.invalidateQueries({ queryKey: queries.members().queryKey });
    },
    onError: (value) => setError(normalizeAPIError(value).message),
  });
  return (
    <section aria-labelledby="members-title">
      <h2 id="members-title">Members</h2>
      <p className="muted">
        Disabling a member signs them out everywhere, revokes their tokens, and
        stops their Capsules' model traffic.
      </p>
      {error ? <p role="alert">{error}</p> : null}
      <ul>
        {(members.data?.items ?? []).map((item) => {
          const name = item.displayName || item.email || item.id;
          return (
            <li key={item.id}>
              <span>
                {name}
                {item.id === selfId ? " (you)" : ""} · {item.role}
                {item.disabled ? " · disabled" : ""}
              </span>
              <button
                type="button"
                aria-label={`${item.role === "admin" ? "Remove admin" : "Make admin"}: ${name}`}
                disabled={change.isPending}
                onClick={() =>
                  change.mutate(() =>
                    api.setMemberRole(item.id, item.role === "admin" ? "member" : "admin"),
                  )
                }
              >
                {item.role === "admin" ? "Remove admin" : "Make admin"}
              </button>
              <button
                type="button"
                aria-label={`${item.disabled ? "Enable" : "Disable"}: ${name}`}
                disabled={change.isPending}
                onClick={() =>
                  change.mutate(() =>
                    item.disabled ? api.enableMember(item.id) : api.disableMember(item.id),
                  )
                }
              >
                {item.disabled ? "Enable" : "Disable"}
              </button>
              <button
                type="button"
                aria-label={`Sign out everywhere: ${name}`}
                disabled={change.isPending}
                onClick={() => change.mutate(() => api.revokeMemberCredentials(item.id))}
              >
                Sign out everywhere
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
