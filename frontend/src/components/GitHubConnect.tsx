import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import { isUnauthenticated } from "./Authentication";
import type { GitHubConnection, GitHubDevice } from "../api/generated/types.gen";
import { Button } from "./ui/Button";

export const githubConnectionQuery = {
  queryKey: ["github-connection"],
  queryFn: ({ signal }: { signal?: AbortSignal }) => api.githubConnection(signal),
};

export function githubReason(reason?: GitHubConnection["reason"]) {
  switch (reason) {
    case "expired":
      return "The GitHub login expired.";
    case "missing_access":
      return "The GitHub login is missing repository access.";
    default:
      return "The GitHub login is no longer valid.";
  }
}

export function GitHubReconnectBanner() {
  const cache = useQueryClient();
  const connection = useQuery({ ...githubConnectionQuery, staleTime: 60_000, retry: false });
  const check = useQuery({
    queryKey: ["github-connection-check"],
    queryFn: async () => {
      const next = await api.checkGitHubConnection();
      cache.setQueryData(githubConnectionQuery.queryKey, next);
      return next;
    },
    enabled: connection.data?.connected === true,
    staleTime: 5 * 60_000,
    retry: false,
  });
  const status = check.data ?? connection.data;
  if (status?.status !== "needs_reconnect") return null;
  return (
    <div className="github-reconnect" role="status">
      <p>
        <strong>Reconnect GitHub.</strong> {githubReason(status.reason)} Clone, push, and pull requests that use this
        login wait until you connect again.
      </p>
      <Link to="/ui/settings/account#github">Reconnect GitHub</Link>
    </div>
  );
}

// Connect GitHub once. The page never sees a token: the station reads a
// confirmed host login, or runs the device flow and keeps the token there.
export function GitHubConnect() {
  const cache = useQueryClient();
  const connection = useQuery({ ...githubConnectionQuery, retry: false });
  const host = useQuery({
    queryKey: ["github-host-login"],
    queryFn: ({ signal }) => api.hostGitHubLogin(signal),
    enabled: connection.isSuccess && connection.data.status !== "active",
    retry: false,
  });
  const [device, setDevice] = useState<GitHubDevice | null>(null);
  const [deviceError, setDeviceError] = useState("");
  const version = connection.data?.resourceVersion ?? 0;

  const refresh = async () => {
    await cache.invalidateQueries({ queryKey: ["github-connection"] });
    await cache.invalidateQueries({ queryKey: ["github-connection-check"] });
  };

  const useHost = useMutation({
    mutationFn: () => api.useHostGitHub(version),
    onSuccess: async () => {
      setDevice(null);
      await refresh();
    },
  });
  const startDevice = useMutation({
    mutationFn: () => api.beginGitHubDevice(version),
    onSuccess: (started) => {
      setDeviceError("");
      setDevice(started);
    },
  });
  const disconnect = useMutation({
    mutationFn: () => api.disconnectGitHub(version),
    onSuccess: async () => {
      setDevice(null);
      await refresh();
    },
  });

  useEffect(() => {
    if (!device) return;
    let stopped = false;
    let timer = 0;
    const poll = async () => {
      try {
        const status = await api.pollGitHubDevice();
        if (stopped) return;
        if (status.status === "connected") {
          setDevice(null);
          await refresh();
          return;
        }
        if (status.status === "denied" || status.status === "expired") {
          setDevice(null);
          setDeviceError(status.status === "denied" ? "GitHub authorization was declined." : "That code expired. Try again.");
          return;
        }
        timer = window.setTimeout(poll, device.intervalSeconds * 1000);
      } catch (error) {
        if (!stopped) setDeviceError(normalizeAPIError(error).message);
      }
    };
    timer = window.setTimeout(poll, device.intervalSeconds * 1000);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [device]);

  if (connection.isPending || (connection.isSuccess && connection.data.status !== "active" && host.isLoading)) {
    return <p role="status">Checking GitHub…</p>;
  }
  if (isUnauthenticated(connection.error)) return null;
  if (connection.isError) return <p>{normalizeAPIError(connection.error).message}</p>;
  if (host.isError) return <p role="alert">{normalizeAPIError(host.error).message}</p>;

  const current = connection.data;
  const hostLogin = host.data?.state === "signed_in" && host.data.available ? host.data.login : undefined;
  if (current.connected && current.status === "active") {
    return (
      <div className="github-connect">
        <p className="is-ready">
          Connected as @{current.login}. Clone, push, pull requests, and agents in a Capsule use this login. Capsule
          access is on unless you turn it off under What Capsules inherit.
        </p>
        <Button type="button" disabled={disconnect.isPending} onClick={() => disconnect.mutate()}>
          {disconnect.isPending ? "Disconnecting…" : "Disconnect"}
        </Button>
        {disconnect.isError && <p role="alert">{normalizeAPIError(disconnect.error).message}</p>}
      </div>
    );
  }

  return (
    <div className="github-connect">
      {current.status === "needs_reconnect" && (
        <p role="status">{githubReason(current.reason)} Connect again to keep using @{current.login}.</p>
      )}
      {!current.connected && (
        <p>
          Connect once. Private repositories, push, pull requests, and agents in a Capsule use this login. The token
          stays on the Station.
        </p>
      )}
      {hostLogin ? (
        <div className="github-actions">
          {host.data?.detail ? <p className="github-status">{host.data.detail}</p> : null}
          <Button type="button" variant="primary" disabled={useHost.isPending} onClick={() => useHost.mutate()}>
            {useHost.isPending ? "Connecting…" : `Use your GitHub CLI login (@${hostLogin})`}
          </Button>
        </div>
      ) : (
        <div className="github-actions">
          <p className="github-status">
            {host.data?.detail || "The GitHub CLI could not be used from this Station."}
          </p>
          {device ? (
            <DeviceStep
              device={device}
              onCancel={() => {
                setDevice(null);
                void api.cancelGitHubDevice().catch(() => undefined);
              }}
            />
          ) : current.deviceFlow ? (
            <Button
              type="button"
              variant="secondary"
              disabled={startDevice.isPending}
              onClick={() => startDevice.mutate()}
            >
              {startDevice.isPending ? "Starting…" : "Connect with a device code"}
            </Button>
          ) : (
            <p className="field-hint">
              This Station has no GitHub OAuth client id, so a device code is unavailable. On your computer, run{" "}
              <code>meridian github connect</code>.
            </p>
          )}
        </div>
      )}
      {(useHost.isError || startDevice.isError || deviceError) && (
        <p role="alert">{deviceError || normalizeAPIError(useHost.error ?? startDevice.error).message}</p>
      )}
    </div>
  );
}

function DeviceStep({ device, onCancel }: { device: GitHubDevice; onCancel: () => void }) {
  return (
    <div className="github-device">
      <p>Enter this code on GitHub. This page picks up the approval on its own.</p>
      <p className="github-code" aria-label="GitHub device code">
        {device.userCode}
      </p>
      <p>
        <a href={device.verificationUri} target="_blank" rel="noreferrer">
          Open github.com/login/device
        </a>
      </p>
      <p role="status">Waiting for GitHub…</p>
      <Button type="button" onClick={onCancel}>
        Cancel
      </Button>
    </div>
  );
}
