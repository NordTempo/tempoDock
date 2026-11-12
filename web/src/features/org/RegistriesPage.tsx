import { useSuspenseQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useIsAdmin } from "../../data/queries";
import { useShortcuts } from "../../shell/keys";
import { since } from "../../ui/format";
import { Act, Button, Confirm, ErrorText, Page, Panel } from "../../ui/kit";
import { useValidation } from "../create/form";
import {
  orgQueries,
  useDeleteRegistryCredential,
  useSetRegistryCredential,
} from "./data";
import { OrgSettingsBar } from "./SettingsBar";

/**
 * The organization's registry credentials (`dock registry credentials`,
 * ADR 0051): deploys of images from a host listed here pull with its
 * username and password. Admins only: the API refuses everyone else.
 * Passwords are write-only.
 */
export function RegistriesPage() {
  if (!useIsAdmin()) {
    return (
      <>
        <OrgSettingsBar />
        <Page>
          <p className="text-mut">
            Only the organization's admins see registry credentials.
          </p>
        </Page>
      </>
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
  const [adding, setAdding] = useState(false);
  useShortcuts([
    { key: "n", label: "add registry login", run: () => setAdding(true) },
  ]);
  return (
    <>
      <OrgSettingsBar
        actions={
          !adding && (
            <Button primary onClick={() => setAdding(true)}>
              Add registry login <kbd>N</kbd>
            </Button>
          )
        }
      />
      <Page>
        {creds.length === 0 ? (
          !adding && (
            <p className="text-mut">
              No registry logins. Deploys pull every image anonymously. Add one
              with Add registry login.
            </p>
          )
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
                      setAdding(true);
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
        {adding && (
          <CredentialForm
            host={host}
            username={username}
            password={password}
            onHost={setHost}
            onUsername={setUsername}
            onClose={() => {
              setAdding(false);
              setHost("");
              setUsername("");
            }}
          />
        )}
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
  const [asking, setAsking] = useState(false);
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
            danger
            small
            cli={`dock registry logout ${host}`}
            disabled={remove.isPending}
            onClick={() => setAsking(true)}
          >
            Remove
          </Act>
        </span>
        {asking && (
          <Confirm
            danger
            action={`Remove the login for ${host}`}
            cli={`dock registry logout ${host}`}
            pending={remove.isPending}
            error={remove.error}
            onConfirm={() =>
              remove.mutate(host, { onSuccess: () => setAsking(false) })
            }
            onCancel={() => setAsking(false)}
          >
            Deploys of private images from {host} fail until a login is added
            again. Services already running keep running.
          </Confirm>
        )}
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
  onClose,
}: {
  host: string;
  username: string;
  password: React.RefObject<HTMLInputElement | null>;
  onHost: (v: string) => void;
  onUsername: (v: string) => void;
  onClose: () => void;
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
              setSecret("");
              onClose();
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
        className={`field w-48 font-mono ${mark(!host.trim())}`}
      />
      <input
        aria-label="Username"
        placeholder="username"
        value={username}
        onChange={(e) => onUsername(e.target.value)}
        spellCheck={false}
        autoComplete="off"
        aria-invalid={!!mark(!username)}
        className={`field w-40 font-mono ${mark(!username)}`}
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
        className={`field min-w-0 flex-[1_1_180px] font-mono ${mark(!secret)}`}
      />
      <Act
        type="submit"
        cli={`dock registry login ${host.trim() || "HOST"} -u ${username || "NAME"} --password-stdin`}
        disabled={set.isPending}
      >
        Save
      </Act>
      <Button onClick={onClose}>Cancel</Button>
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
