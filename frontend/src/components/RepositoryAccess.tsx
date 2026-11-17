import { type FormEvent, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule } from "../api/generated/types.gen";
import { GitHubConnect, githubConnectionQuery } from "./GitHubConnect";
import { repositoryAccessCopy } from "./startup";
import { Button } from "./ui/Button";

function tokenError(error: unknown) {
  const message = normalizeAPIError(error).message;
  if (/internal server error/i.test(message) || message.includes("://") || message.includes("@")) {
    return "The token could not be saved.";
  }
  return message;
}

export function RepositoryAccessNotice({
  message,
  capsule,
}: {
  message?: string;
  capsule?: Pick<Capsule, "id" | "resourceVersion">;
}) {
  const client = useQueryClient();
  const connection = useQuery({ ...githubConnectionQuery, retry: false });
  const connected = connection.data?.status === "active";
  const [connecting, setConnecting] = useState(false);
  const [open, setOpen] = useState(false);
  const [token, setToken] = useState("");
  const save = useMutation({
    mutationFn: async () => {
      const current = await api.githubConnection().catch(() => undefined);
      const version = current?.connected ? current.resourceVersion ?? 0 : 0;
      await api.connectGitHubToken(token.trim(), version);
    },
    onSuccess: async () => {
      setToken("");
      setOpen(false);
      await client.invalidateQueries({ queryKey: ["github-connection"] });
      if (!capsule) return;
      await client.invalidateQueries({ queryKey: ["capsule", capsule.id] });
      await client.invalidateQueries({ queryKey: ["project-thread-intent"] });
    },
  });
  const retry = useMutation({
    mutationFn: async () => {
      if (!capsule) return;
      await api.lifecycle("retry", capsule.id, capsule.resourceVersion);
    },
    onSuccess: async () => {
      if (!capsule) return;
      await client.invalidateQueries({ queryKey: ["capsule", capsule.id] });
      await client.invalidateQueries({ queryKey: ["project-thread-intent"] });
    },
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    save.mutate();
  }

  return (
    <div className="access-failure">
      <p className="startup-reason" role="alert">{repositoryAccessCopy(message)}</p>
      {connected && (
        <p role="status">
          {capsule
            ? "GitHub is connected. Retry to clone again."
            : "GitHub is connected. Create the Project again."}
        </p>
      )}
      <p className="startup-actions">
        {connected && capsule ? (
          <Button type="button" variant="primary" disabled={retry.isPending} onClick={() => retry.mutate()}>
            {retry.isPending ? "Retrying…" : "Retry"}
          </Button>
        ) : !connected ? (
          <Button type="button" variant="primary" onClick={() => setConnecting((value) => !value)}>
            Connect GitHub
          </Button>
        ) : null}
        <Button type="button" variant="secondary" onClick={() => setOpen((value) => !value)}>
          Add a token
        </Button>
      </p>
      {connecting && !connected && <GitHubConnect />}
      {open && (
        <form className="access-token-form" onSubmit={submit}>
          <label htmlFor={capsule ? `access-token-${capsule.id}` : "access-token"}>
            GitHub token
            <input
              id={capsule ? `access-token-${capsule.id}` : "access-token"}
              type="password"
              autoComplete="off"
              spellCheck={false}
              required
              minLength={8}
              maxLength={2048}
              value={token}
              disabled={save.isPending}
              onChange={(event) => setToken(event.target.value)}
            />
          </label>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save token"}
          </Button>
        </form>
      )}
      {save.isError && <p role="alert">{tokenError(save.error)}</p>}
      {retry.isError && <p role="alert">{normalizeAPIError(retry.error).message}</p>}
      {save.isSuccess && !capsule && !connected && <p role="status">Token saved. Create the Project again.</p>}
    </div>
  );
}
