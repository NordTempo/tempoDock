import { useSuspenseQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import type { components } from "../../api/schema";
import { useSession } from "../../data/queries";
import { useShortcuts } from "../../shell/keys";
import { day } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Glyph,
  Page,
  PageBar,
} from "../../ui/kit";
import { kindName, ProviderMark } from "../sso/ProviderMark";
import {
  type Provider,
  signInSettingsQuery,
  ssoProvidersQuery,
  useCreateSSOProvider,
  useDeleteSSOProvider,
  useSetSignInSettings,
  useUpdateSSOProvider,
} from "./signIn";

// The console's Sign-in page (ADR 0065, `dock sso`): the platform's
// identity providers as a list beside the open one, under a strip with
// the two switches (password sign-in, invite only) and the one redirect
// URL every provider sends people back to. Chosen layout C of the
// 2026-11-07 mocks.

type Kind = Provider["kind"];

/** What the right-hand side shows: a provider, its form, or a new one's. */
type Pane =
  | { mode: "view"; name: string }
  | { mode: "edit"; name: string }
  | { mode: "add" };

export function SignInPage() {
  const data = useSuspenseQuery(ssoProvidersQuery()).data;
  const providers = data.providers ?? [];
  const redirect = data.redirect_url;
  const [pane, setPane] = useState<Pane | undefined>();
  useShortcuts([
    { key: "n", label: "add a provider", run: () => setPane({ mode: "add" }) },
  ]);
  // The first provider is open until another is picked; with none, the
  // form to add one.
  const shown: Pane =
    pane ??
    (providers[0]
      ? { mode: "view", name: providers[0].name }
      : { mode: "add" });
  const open =
    shown.mode === "add"
      ? undefined
      : providers.find((p) => p.name === shown.name);
  const selected = shown.mode === "add" ? undefined : shown.name;
  return (
    <>
      <PageBar
        title="Sign-in"
        actions={
          <Act
            primary
            cli="dock sso add"
            onClick={() => setPane({ mode: "add" })}
          >
            Add provider <kbd>N</kbd>
          </Act>
        }
      />
      <Page gap="gap-0">
        <Switches redirect={redirect} hasProviders={providers.length > 0} />
        <div className="flex flex-col lg:flex-row">
          {providers.length > 0 && (
            <ul
              aria-label="Identity providers"
              className="flex flex-col border-line lg:w-[280px] lg:shrink-0 lg:border-r"
            >
              {providers.map((p) => (
                <li key={p.name}>
                  <button
                    type="button"
                    aria-current={p.name === selected || undefined}
                    onClick={() => setPane({ mode: "view", name: p.name })}
                    className="flex w-full items-center gap-2.5 border-b border-soft px-3 py-2.5 text-left hover:bg-hover aria-[current]:bg-sel"
                  >
                    <ProviderMark kind={p.kind} />
                    <span className="min-w-0 flex-1 truncate">{p.label}</span>
                    <span
                      className="font-mono text-xs text-faint"
                      title="People linked"
                    >
                      {p.linked}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          <section
            className={`flex min-w-0 flex-1 flex-col gap-4 py-5 ${providers.length > 0 ? "lg:px-6" : ""}`}
          >
            {shown.mode === "add" || !open ? (
              <ProviderForm
                redirect={redirect}
                onDone={(name) =>
                  setPane(name ? { mode: "view", name } : undefined)
                }
                cancellable={providers.length > 0}
              />
            ) : shown.mode === "edit" ? (
              <ProviderForm
                provider={open}
                redirect={redirect}
                onDone={() => setPane({ mode: "view", name: open.name })}
                cancellable
              />
            ) : (
              <ProviderView
                provider={open}
                redirect={redirect}
                last={providers.length === 1}
                onEdit={() => setPane({ mode: "edit", name: open.name })}
                onRemoved={() => setPane(undefined)}
              />
            )}
          </section>
        </div>
      </Page>
    </>
  );
}

/**
 * The strip under the bar: password sign-in and invite only, each with
 * its state and the one button that flips it, and the redirect URL.
 */
function Switches({
  redirect,
  hasProviders,
}: {
  redirect: string;
  hasProviders: boolean;
}) {
  const set = useSuspenseQuery(signInSettingsQuery()).data;
  const change = useSetSignInSettings();
  const me = useSession();
  const [asking, setAsking] = useState<"password" | "invite" | null>(null);
  const close = () => {
    change.reset();
    setAsking(null);
  };
  // Why passwords can't go off yet; the API says the same.
  const passwordBlocked = !set.password_sign_in
    ? undefined
    : !hasProviders
      ? "add a provider first"
      : !me?.sso
        ? "sign in with a provider yourself first"
        : undefined;
  return (
    <div className="flex flex-col gap-2 border-b border-line pb-4">
      <div className="flex flex-wrap items-center gap-x-8 gap-y-2">
        <span className="flex flex-wrap items-center gap-2">
          Password sign-in <OnOff on={set.password_sign_in} />
          <Act
            small
            cli={`dock sso password-sign-in ${set.password_sign_in ? "off" : "on"}`}
            disabled={change.isPending || !!passwordBlocked}
            reason={passwordBlocked}
            onClick={() =>
              set.password_sign_in
                ? setAsking("password")
                : change.mutate({ password_sign_in: true })
            }
          >
            Turn {set.password_sign_in ? "off" : "on"}
          </Act>
        </span>
        <span className="flex flex-wrap items-center gap-2">
          Invite only <OnOff on={set.invite_only} />
          <Act
            small
            cli={`dock sso invite-only ${set.invite_only ? "off" : "on"}`}
            disabled={change.isPending}
            onClick={() =>
              set.invite_only
                ? setAsking("invite")
                : change.mutate({ invite_only: true })
            }
          >
            Turn {set.invite_only ? "off" : "on"}
          </Act>
        </span>
        <span className="flex min-w-0 flex-wrap items-center gap-2 text-mut">
          Redirect URL
          <code className="min-w-0 font-mono text-xs text-sec [overflow-wrap:anywhere]">
            {redirect}
          </code>
          <CopyButton text={redirect} />
        </span>
      </div>
      <p className="text-xs text-mut">
        {set.invite_only
          ? "Providers sign in people with an account here, and make accounts only for people with an open invite."
          : "Providers make an account for anyone they sign in, in no organization until an admin invites or adds them."}
        {!set.password_sign_in &&
          " Passwords are off; to turn them back on from the server: "}
        {!set.password_sign_in && (
          <code className="font-mono text-fg">
            sudo dock admin password-sign-in on
          </code>
        )}
      </p>
      {!asking && <ErrorText error={change.error} />}
      {asking === "password" && (
        <Confirm
          action="Turn off password sign-in"
          cli="dock sso password-sign-in off"
          pending={change.isPending}
          error={change.error}
          onConfirm={() =>
            change.mutate({ password_sign_in: false }, { onSuccess: close })
          }
          onCancel={close}
        >
          The sign-in page shows only the providers and passkeys, and invite
          links make no password accounts. People signed in now stay signed in.
          If every provider breaks, someone with shell access to the server runs{" "}
          <b className="font-mono">sudo dock admin password-sign-in on</b>.
        </Confirm>
      )}
      {asking === "invite" && (
        <Confirm
          action="Turn off invite only"
          cli="dock sso invite-only off"
          pending={change.isPending}
          error={change.error}
          onConfirm={() =>
            change.mutate({ invite_only: false }, { onSuccess: close })
          }
          onCancel={close}
        >
          Anyone a provider signs in gets an account, in no organization. Like
          any account, they can create an organization of their own and deploy
          to your servers. Keep invite only on unless your providers sign in
          only your own people.
        </Confirm>
      )}
    </div>
  );
}

/** A switch's state: a green dot and on, or a ring and off. */
function OnOff({ on }: { on: boolean }) {
  return on ? (
    <span className="text-ok">
      <Glyph char="●" /> on
    </span>
  ) : (
    <span className="text-mut">
      <Glyph char="○" /> off
    </span>
  );
}

/** Where to register Dock at each kind of provider. */
function setupHint(kind: Kind) {
  switch (kind) {
    case "google":
      return "In Google Cloud: APIs & Services, Credentials, Create credentials, OAuth client ID, Web application. Add the redirect URL as an authorized redirect URI.";
    case "github":
      return "At GitHub: Settings, Developer settings, OAuth Apps, New OAuth App. The redirect URL is its authorization callback URL. Dock signs people in with the account's primary verified email.";
    case "oidc":
      return "Register a web application with the scopes openid, email and profile and the redirect URL. The issuer must match the provider's tokens exactly, trailing slash included.";
  }
}

/** The open provider: its facts, its button, Edit and Remove. */
function ProviderView({
  provider: p,
  redirect,
  last,
  onEdit,
  onRemoved,
}: {
  provider: Provider;
  redirect: string;
  last: boolean;
  onEdit: () => void;
  onRemoved: () => void;
}) {
  const remove = useDeleteSSOProvider();
  const passwordsOff = !useSuspenseQuery(signInSettingsQuery()).data
    .password_sign_in;
  const [asking, setAsking] = useState(false);
  const stays = last && passwordsOff;
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="flex-1 text-lg font-semibold">{p.label}</h2>
        <Act small cli={`dock sso set ${p.name}`} onClick={onEdit}>
          Edit
        </Act>
        <Act
          small
          danger
          cli={`dock sso remove ${p.name}`}
          disabled={stays}
          reason={
            stays ? "the last provider while passwords are off" : undefined
          }
          onClick={() => setAsking(true)}
        >
          Remove
        </Act>
      </div>
      <dl className="props">
        <dt>Kind</dt>
        <dd>{kindName[p.kind]}</dd>
        <dt>Name</dt>
        <dd className="font-mono">{p.name}</dd>
        {p.kind === "oidc" && (
          <>
            <dt>Issuer</dt>
            <dd className="font-mono [overflow-wrap:anywhere]">{p.issuer}</dd>
          </>
        )}
        <dt>Client ID</dt>
        <dd className="font-mono [overflow-wrap:anywhere]">{p.client_id}</dd>
        <dt>Client secret</dt>
        <dd className="text-mut">set, never shown</dd>
        <dt>People linked</dt>
        <dd>{p.linked}</dd>
        <dt>Added</dt>
        <dd className="text-mut">
          {day(p.created_at)} by {p.created_by}
        </dd>
      </dl>
      <div className="flex flex-col gap-1.5">
        <span className="text-xs text-faint">On the sign-in page</span>
        <span className="flex max-w-[340px] items-center justify-center gap-2.5 rounded-ctl border border-edge px-3.5 py-2.5 font-medium">
          <ProviderMark kind={p.kind} />
          Continue with {p.label}
        </span>
      </div>
      <p className="max-w-[760px] text-xs text-mut">
        {setupHint(p.kind)} Redirect URL:{" "}
        <code className="font-mono text-sec">{redirect}</code>
      </p>
      {asking && (
        <Confirm
          danger
          action={`Remove ${p.label}`}
          cli={`dock sso remove ${p.name}`}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate(p.name, { onSuccess: onRemoved })}
          onCancel={() => {
            remove.reset();
            setAsking(false);
          }}
        >
          The sign-in page loses <b>Continue with {p.label}</b>, and the{" "}
          {p.linked === 1 ? "person" : `${p.linked} people`} signed in through
          it are signed out. They keep their accounts and memberships, and sign
          in another way.
        </Confirm>
      )}
    </>
  );
}

const kinds: { kind: Kind; label: string }[] = [
  { kind: "google", label: "Google" },
  { kind: "github", label: "GitHub" },
  { kind: "oidc", label: "Custom" },
];

/** The default label of a kind's button; a custom one needs its own. */
const defaultLabel: Record<Kind, string> = {
  google: "Google",
  github: "GitHub",
  oidc: "",
};

/**
 * Adding a provider (a preset, then the client) or changing one. A blank
 * client secret keeps the one stored.
 */
function ProviderForm({
  provider,
  redirect,
  onDone,
  cancellable,
}: {
  provider?: Provider;
  redirect: string;
  /** With the new provider's name after adding; none on cancel. */
  onDone: (name?: string) => void;
  cancellable: boolean;
}) {
  const create = useCreateSSOProvider();
  const update = useUpdateSSOProvider();
  const action = provider ? update : create;
  const [kind, setKind] = useState<Kind>(provider?.kind ?? "google");
  const [name, setName] = useState("");
  const [label, setLabel] = useState(provider?.label ?? "");
  const [issuer, setIssuer] = useState(provider?.issuer ?? "");
  const [clientID, setClientID] = useState(provider?.client_id ?? "");
  const [secret, setSecret] = useState("");
  const custom = kind === "oidc";
  const finalName = name.trim() || (custom ? "" : kind);

  const changed: components["schemas"]["UpdateSSOProviderRequest"] = provider
    ? {
        ...(label.trim() !== provider.label && { label: label.trim() }),
        ...(custom &&
          issuer.trim() !== (provider.issuer ?? "") && {
            issuer: issuer.trim(),
          }),
        ...(clientID.trim() !== provider.client_id && {
          client_id: clientID.trim(),
        }),
        ...(secret && { client_secret: secret }),
      }
    : {};
  const ready = provider
    ? Object.keys(changed).length > 0 && !!label.trim() && !!clientID.trim()
    : !!finalName &&
      !!clientID.trim() &&
      !!secret &&
      (!custom || (!!issuer.trim() && !!label.trim()));
  const flags = [
    !provider && custom && "--kind oidc",
    custom &&
      (!provider || changed.issuer !== undefined) &&
      `--issuer ${issuer.trim() || "<url>"}`,
    ((!provider && label.trim()) || changed.label !== undefined) &&
      `--label "${label.trim()}"`,
    (!provider || changed.client_id !== undefined) &&
      `--client-id ${clientID.trim() || "<id>"}`,
    (!provider || changed.client_secret) && "--client-secret-stdin",
  ]
    .filter(Boolean)
    .join(" ");
  const cli = provider
    ? `dock sso set ${provider.name} ${flags}`
    : `dock sso add ${finalName || "<name>"} ${flags}`;

  return (
    <form
      aria-label={provider ? `Change ${provider.label}` : "Add a provider"}
      className="flex max-w-[640px] flex-col gap-3.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (!ready) return;
        if (provider) {
          update.mutate(
            { name: provider.name, ...changed },
            { onSuccess: () => onDone(provider.name) },
          );
          return;
        }
        create.mutate(
          {
            name: finalName,
            kind,
            ...(label.trim() && { label: label.trim() }),
            ...(custom && { issuer: issuer.trim() }),
            client_id: clientID.trim(),
            client_secret: secret,
          },
          { onSuccess: (p) => onDone(p.name) },
        );
      }}
    >
      <h2 className="text-lg font-semibold">
        {provider ? `Change ${provider.label}` : "Add a provider"}
      </h2>
      {!provider && (
        <div role="radiogroup" aria-label="Kind" className="flex gap-5">
          {kinds.map((k) => (
            <label
              key={k.kind}
              className={`flex cursor-pointer items-center gap-2 border-b-2 pb-1.5 ${kind === k.kind ? "border-fg text-fg" : "border-transparent text-mut hover:text-fg"}`}
            >
              <input
                type="radio"
                name="kind"
                className="sr-only"
                checked={kind === k.kind}
                onChange={() => {
                  setKind(k.kind);
                  setLabel("");
                }}
              />
              <ProviderMark kind={k.kind} />
              {k.label}
            </label>
          ))}
        </div>
      )}
      <p className="text-xs text-mut">
        {setupHint(kind)} Redirect URL:{" "}
        <code className="font-mono text-sec [overflow-wrap:anywhere]">
          {redirect}
        </code>
      </p>
      {!provider && (
        <Field label="Name" note="for the CLI: lowercase, digits, dashes">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={custom ? "corp" : kind}
            className="field font-mono"
          />
        </Field>
      )}
      {custom && (
        <Field label="Issuer URL">
          <input
            type="url"
            value={issuer}
            onChange={(e) => setIssuer(e.target.value)}
            placeholder="https://id.example.com/realms/staff"
            className="field font-mono"
          />
        </Field>
      )}
      <Field label="Button" note="Continue with ...">
        <input
          value={label}
          maxLength={40}
          onChange={(e) => setLabel(e.target.value)}
          placeholder={defaultLabel[kind] || "Example ID"}
          className="field"
        />
      </Field>
      <Field label="Client ID">
        <input
          value={clientID}
          onChange={(e) => setClientID(e.target.value)}
          className="field font-mono"
        />
      </Field>
      <Field
        label="Client secret"
        note={provider ? "blank keeps it" : "never shown again"}
      >
        <input
          type="password"
          autoComplete="off"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          placeholder={provider ? "unchanged" : undefined}
          className="field font-mono"
        />
      </Field>
      <div className="flex flex-wrap items-center gap-2 border-t border-line pt-3">
        <span className="min-w-0 flex-1 text-xs">
          {action.error ? (
            <ErrorText error={action.error} />
          ) : (
            <code className="font-mono text-2xs text-mut [overflow-wrap:anywhere]">
              {cli}
            </code>
          )}
        </span>
        {cancellable && (
          <Button
            onClick={() => {
              action.reset();
              onDone(provider?.name);
            }}
          >
            Cancel
          </Button>
        )}
        <button
          type="submit"
          className="btn pri"
          disabled={!ready || action.isPending}
        >
          {provider
            ? "Save"
            : `Add ${label.trim() || defaultLabel[kind] || "provider"}`}
        </button>
      </div>
    </form>
  );
}

/** A labeled form field with a faint note after the label. */
function Field({
  label,
  note,
  children,
}: {
  label: string;
  note?: string;
  children: ReactNode;
}) {
  return (
    // biome-ignore lint/a11y/noLabelWithoutControl: the control is a child
    <label className="flex min-w-0 flex-col gap-1.5">
      <span>
        {label} {note && <span className="text-xs text-faint">{note}</span>}
      </span>
      {children}
    </label>
  );
}
