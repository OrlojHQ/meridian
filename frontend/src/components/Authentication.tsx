import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import { api, normalizeAPIError } from "../api/client";
import { queries } from "../api/queries";

// In team mode each member signs in through the identity provider. The
// daemon serves this path itself and redirects to the configured issuer
// (ADR 0046).
export const TEAM_SIGN_IN_PATH = "/auth/oidc/start";

// isUnauthenticated reports whether a request failed because the daemon
// rejected this browser's session.
export function isUnauthenticated(error: unknown): boolean {
  return error != null && normalizeAPIError(error).status === 401;
}

export function AuthenticationState() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const mode = useQuery(queries.authenticationMode());
  const hostname = window.location.hostname;
  const safeOrigin =
    window.location.protocol === "https:" ||
    hostname === "localhost" ||
    hostname === "127.0.0.1" ||
    hostname === "::1" ||
    hostname === "[::1]";
  if (!safeOrigin) {
    return (
      <section className="error-state" role="alert">
        <h2>HTTPS required</h2>
        <p>
          Remote browser authentication is available only over HTTPS. Use a
          loopback port-forward or configure an operator-controlled TLS proxy.
        </p>
      </section>
    );
  }
  // Wait for the mode so a team installation never flashes the token form.
  if (mode.isPending) return <p role="status">Checking how this Station signs in…</p>;
  // In team mode each member signs in with the identity provider; the
  // installation token no longer opens a browser session (ADR 0046).
  if (mode.data?.mode === "team") {
    return (
      <section className="error-state sign-in-state" role="alert">
        <h2>Sign in</h2>
        <p>Sign in with your organization's identity provider to use Meridian.</p>
        <a className="button-link primary-button" href={TEAM_SIGN_IN_PATH}>
          Sign in
        </a>
      </section>
    );
  }
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
      await api.authenticateBrowser(token);
      form.reset();
      window.location.reload();
    } catch (value) {
      form.reset();
      setError(normalizeAPIError(value).message);
      setSubmitting(false);
    }
  }
  return (
    <section className="error-state" role="alert">
      <h2>Authentication required</h2>
      <p>
        Enter the installation API token. Meridian exchanges it for an HttpOnly
        same-origin session that lasts until you have not used this Station
        for seven days, and does not store the token in browser storage.
      </p>
      <form onSubmit={submit}>
        <label htmlFor="installation-token">Installation token</label>
        <input
          id="installation-token"
          name="token"
          type="password"
          autoComplete="off"
          required
        />
        <button type="submit" disabled={submitting}>
          {submitting ? "Authenticating…" : "Authenticate"}
        </button>
      </form>
      {error ? <p>{error}</p> : null}
    </section>
  );
}
