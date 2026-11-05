import { useSuspenseQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useIsAdmin } from "../../data/queries";
import { plural, since } from "../../ui/format";
import { Act, Button, ErrorText, Page, PageBar, Panel } from "../../ui/kit";
import { useValidation } from "../create/form";
import {
  orgQueries,
  useDeleteRegistryCredential,
  useOrgName,
  useSetRegistryCredential,
} from "./data";

/**
 * The organization's registry credentials (`dock registry credentials`,
 * ADR 0051): deploys of images from a host listed here pull with its
 * username and password. Admins only: the API refuses everyone else.
 * Passwords are write-only.
 */
export function RegistriesPage() {
  const org = useOrgName();
  if (!useIsAdmin()) {
    return (
      <>
        <PageBar parent={org} title="Registries" />
        <Page>
          <p className="text-mut">
            Only the organization's admins see registry credentials.
          </p>
        </Page>
      </>
    );
  }
  return <Registries org={org} />;
}

function Registries({ org }: { org: string }) {
  const creds = useSuspenseQuery(orgQueries.registryCredentials()).data;
  // The form's host and username; Replace on a row fills them in.
  const [host, setHost] = useState("");
  const [username, setUsername] = useState("");
  const password = useRef<HTMLInputElement>(null);
  return (
    <>
      <PageBar
        parent={org}
        title="Registries"
        after={
          <span className="text-xs text-faint">
            {plural(creds.length, "registry", "registries")}
          </span>
        }
      />
      <Page width="max-w-[900px]" gap="gap-5">
        {creds.length === 0 ? (
          <p className="text-mut">
            No registry logins. Deploys pull every image anonymously.
          </p>
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
    </>
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
  const v = useValidation();
  // Each empty field, marked once a submit was tried.
  const mark = (empty: boolean) =>
    v.required(empty, "missing") ? "border-fail" : "";
  return (
    <form
      ref={v.form}
      aria-label="Add registry credentials"
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (!v.check(!!host.trim() && !!username && !!secret)) return;
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
        aria-invalid={!!mark(!host.trim())}
        className={`field w-48 py-[7px] font-mono ${mark(!host.trim())}`}
      />
      <input
        aria-label="Username"
        placeholder="username"
        value={username}
        onChange={(e) => onUsername(e.target.value)}
        spellCheck={false}
        autoComplete="off"
        aria-invalid={!!mark(!username)}
        className={`field w-40 py-[7px] font-mono ${mark(!username)}`}
      />
      <input
        ref={password}
        aria-label="Password or token"
        placeholder="password or token"
        type="password"
        value={secret}
        onChange={(e) => setSecret(e.target.value)}
        autoComplete="new-password"
        aria-invalid={!!mark(!secret)}
        className={`field min-w-0 flex-[1_1_180px] py-[7px] font-mono ${mark(!secret)}`}
      />
      <Act
        type="submit"
        cli={`dock registry login ${host.trim() || "HOST"} -u ${username || "NAME"} --password-stdin`}
        disabled={set.isPending}
      >
        Save
      </Act>
      <span role="alert" className="text-xs text-fail">
        {v.problem([
          !host.trim() && "a host",
          !username && "a username",
          !secret && "a password or token",
        ])}
      </span>
      <ErrorText error={set.error} />
    </form>
  );
}
