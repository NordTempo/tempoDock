import { useSuspenseQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useIsAdmin } from "../../data/queries";
import { since } from "../../ui/format";
import { Act, Button, ErrorText, Page, Panel, TitleRow } from "../../ui/kit";
import {
  orgQueries,
  useDeleteRegistryCredential,
  useSetRegistryCredential,
} from "./data";

/**
 * The organization's registry credentials (`dock registry credentials`,
 * ADR 0051): deploys of images from a host listed here pull with its
 * username and password. Admins only: the API refuses everyone else.
 * Passwords are write-only.
 */
export function RegistriesPage() {
  if (!useIsAdmin()) {
    return (
      <Page>
        <TitleRow title="Registries" />
        <p className="text-mut">
          Only the organization's admins see registry credentials.
        </p>
      </Page>
    );
  }
  return <Registries />;
}

function Registries() {
  const creds = useSuspenseQuery(orgQueries.registryCredentials()).data;
  // The form's host and username; Replace on a row fills them in.
  const [host, setHost] = useState("");
  const [username, setUsername] = useState("");
  const password = useRef<HTMLInputElement>(null);
  return (
    <Page width="max-w-[900px]" gap="gap-5">
      <TitleRow
        title="Registries"
        context="Deploys pull private images with these. Other hosts are pulled anonymously."
      />
      {creds.length === 0 ? (
        <p className="text-mut">No registry credentials yet.</p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Host</th>
                <th>Username</th>
                <th>Updated</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {creds.map((c) => (
                <CredentialRow
                  key={c.host}
                  host={c.host}
                  username={c.username}
                  updated={c.updated_at}
                  by={c.updated_by}
                  onReplace={() => {
                    setHost(c.host);
                    setUsername(c.username);
                    password.current?.focus();
                  }}
                />
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      <CredentialForm
        host={host}
        username={username}
        password={password}
        onHost={setHost}
        onUsername={setUsername}
      />
    </Page>
  );
}

function CredentialRow({
  host,
  username,
  updated,
  by,
  onReplace,
}: {
  host: string;
  username: string;
  updated: string;
  by: string;
  onReplace: () => void;
}) {
  const remove = useDeleteRegistryCredential();
  return (
    <tr>
      <td className="py-2 font-mono">{host}</td>
      <td className="py-2 font-mono">{username}</td>
      <td className="py-2 text-faint" title={by}>
        {since(updated)}
      </td>
      <td className="w-full py-1.5 text-right whitespace-nowrap">
        <span className="inline-flex gap-2">
          <Button small onClick={onReplace}>
            Replace
          </Button>
          <Act
            small
            cli={`dock registry logout ${host}`}
            disabled={remove.isPending}
            onClick={() => remove.mutate(host)}
          >
            Remove
          </Act>
        </span>
        <ErrorText error={remove.error} />
      </td>
    </tr>
  );
}

/** Adds a credential, or replaces the one for the same host. */
function CredentialForm({
  host,
  username,
  password,
  onHost,
  onUsername,
}: {
  host: string;
  username: string;
  password: React.RefObject<HTMLInputElement | null>;
  onHost: (v: string) => void;
  onUsername: (v: string) => void;
}) {
  const set = useSetRegistryCredential();
  const [secret, setSecret] = useState("");
  return (
    <form
      aria-label="Add registry credentials"
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        set.mutate(
          { host: host.trim(), username, password: secret },
          {
            onSuccess: () => {
              onHost("");
              onUsername("");
              setSecret("");
            },
          },
        );
      }}
    >
      <input
        aria-label="Host"
        placeholder="ghcr.io"
        value={host}
        onChange={(e) => onHost(e.target.value)}
        spellCheck={false}
        className="field w-48 py-[7px] font-mono"
      />
      <input
        aria-label="Username"
        placeholder="username"
        value={username}
        onChange={(e) => onUsername(e.target.value)}
        spellCheck={false}
        autoComplete="off"
        className="field w-40 py-[7px] font-mono"
      />
      <input
        ref={password}
        aria-label="Password or token"
        placeholder="password or token"
        type="password"
        value={secret}
        onChange={(e) => setSecret(e.target.value)}
        autoComplete="new-password"
        className="field min-w-0 flex-[1_1_180px] py-[7px] font-mono"
      />
      <Act
        type="submit"
        cli={`dock registry login ${host.trim() || "HOST"} -u ${username || "NAME"} --password-stdin`}
        disabled={!host.trim() || !username || !secret || set.isPending}
      >
        Save
      </Act>
      <ErrorText error={set.error} />
    </form>
  );
}
