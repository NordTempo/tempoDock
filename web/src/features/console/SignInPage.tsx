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
      {/* One row per switch, labels in a column, so a disabled switch's
          reason never runs into the next one. */}
      <dl className="grid grid-cols-[max-content_minmax(0,1fr)] items-center gap-x-6 gap-y-1.5 [&>dt]:text-mut">
        <dt>Password sign-in</dt>
        <dd className="flex flex-wrap items-center gap-2">
          <OnOff on={set.password_sign_in} />
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
        </dd>
        <dt>Invite only</dt>
        <dd className="flex flex-wrap items-center gap-2">
          <OnOff on={set.invite_only} />
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
        </dd>
        <dt>Redirect URL</dt>
        <dd className="flex min-w-0 flex-wrap items-center gap-2">
          <code className="min-w-0 font-mono text-xs text-sec [overflow-wrap:anywhere]">
            {redirect}
          </code>
          <CopyButton text={redirect} />
        </dd>
      </dl>
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
const setupHint: Record<Kind, string> = {
  google:
    "In Google Cloud: APIs & Services, Credentials, Create credentials, OAuth client ID, Web application. Add the redirect URL as an authorized redirect URI.",
  github:
    "At GitHub: Settings, Developer settings, OAuth Apps, New OAuth App. The redirect URL is its authorization callback URL. Dock signs people in with the account's primary verified email.",
  microsoft:
    "In Entra ID: App registrations, New registration, single tenant, with the redirect URL as a Web redirect URI; then a client secret, and in Token configuration the optional ID token claims email and xms_edov. Dock signs in only people whose email xms_edov vouches for.",
  gitlab:
    "In GitLab: Edit profile (or a group, or the instance's Admin area), Applications, with the scopes openid, email and profile and the redirect URL.",
  okta: "In Okta: Applications, Create App Integration, OIDC, Web Application, with the redirect URL as a sign-in redirect URI.",
  auth0:
    "In Auth0: Applications, Create Application, Regular Web Application. Add the redirect URL to its Allowed Callback URLs.",
  keycloak:
    "In the realm: Clients, Create client, OpenID Connect, Client authentication on, with the redirect URL as a valid redirect URI.",
  authentik:
    "In authentik: Applications, Create with provider, OAuth2/OpenID, Confidential, with the redirect URL. The realm is the application's slug.",
  oidc: "Register a web application with the scopes openid, email and profile and the redirect URL. The issuer must match the provider's tokens exactly, trailing slash included.",
};

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
        {p.kind !== "google" && p.issuer && (
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
        {setupHint[p.kind]} Redirect URL:{" "}
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

/** The kinds to pick from, in the add form's order. */
const kinds: { kind: Kind; label: string }[] = [
  { kind: "google", label: "Google" },
  { kind: "github", label: "GitHub" },
  { kind: "microsoft", label: "Microsoft" },
  { kind: "gitlab", label: "GitLab" },
  { kind: "okta", label: "Okta" },
  { kind: "auth0", label: "Auth0" },
  { kind: "keycloak", label: "Keycloak" },
  { kind: "authentik", label: "Authentik" },
  { kind: "oidc", label: "Custom" },
];

/** The default label of a kind's button; a custom one needs its own. */
const defaultLabel: Record<Kind, string> = {
  google: "Google",
  github: "GitHub",
  microsoft: "Microsoft",
  gitlab: "GitLab",
  okta: "Okta",
  auth0: "Auth0",
  keycloak: "Keycloak",
  authentik: "Authentik",
  oidc: "",
};

/** What a kind asks for to find its provider (ADR 0081). */
type WhereKey = "tenant" | "domain" | "realm" | "issuer";
type WhereField = {
  key: WhereKey;
  label: string;
  placeholder: string;
  /** Left blank, the kind's default (gitlab.com). */
  optional?: boolean;
};

const whereFields: Record<Kind, WhereField[]> = {
  google: [],
  github: [],
  microsoft: [
    {
      key: "tenant",
      label: "Tenant",
      placeholder: "contoso.onmicrosoft.com or its tenant ID",
    },
  ],
  gitlab: [
    {
      key: "domain",
      label: "GitLab URL",
      placeholder: "gitlab.com",
      optional: true,
    },
  ],
  okta: [{ key: "domain", label: "Okta domain", placeholder: "acme.okta.com" }],
  auth0: [
    { key: "domain", label: "Auth0 domain", placeholder: "acme.eu.auth0.com" },
  ],
  keycloak: [
    { key: "domain", label: "Keycloak URL", placeholder: "id.example.com" },
    { key: "realm", label: "Realm", placeholder: "staff" },
  ],
  authentik: [
    { key: "domain", label: "authentik URL", placeholder: "auth.example.com" },
    { key: "realm", label: "Application slug", placeholder: "dock" },
  ],
  oidc: [
    {
      key: "issuer",
      label: "Issuer URL",
      placeholder: "https://id.example.com/realms/staff",
    },
  ],
};

/** The CLI flag of each field. */
const whereFlag: Record<WhereKey, string> = {
  tenant: "--tenant",
  domain: "--domain",
  realm: "--realm",
  issuer: "--issuer",
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
  const stored: Record<WhereKey, string> = {
    tenant: provider?.tenant ?? "",
    domain: provider?.domain ?? "",
    realm: provider?.realm ?? "",
    issuer: provider?.issuer ?? "",
  };
  const [where, setWhere] = useState(stored);
  const [clientID, setClientID] = useState(provider?.client_id ?? "");
  const [secret, setSecret] = useState("");
  const custom = kind === "oidc";
  const fields = whereFields[kind];
  const finalName = name.trim() || (custom ? "" : kind);
  const whereChanged = fields.filter(
    (f) => where[f.key].trim() !== stored[f.key],
  );
  const whereSet = fields.filter((f) => where[f.key].trim());

  const changed: components["schemas"]["UpdateSSOProviderRequest"] = provider
    ? {
        ...(label.trim() !== provider.label && { label: label.trim() }),
        ...Object.fromEntries(
          whereChanged.map((f) => [f.key, where[f.key].trim()]),
        ),
        ...(clientID.trim() !== provider.client_id && {
          client_id: clientID.trim(),
        }),
        ...(secret && { client_secret: secret }),
      }
    : {};
  const whereReady = fields.every((f) => f.optional || where[f.key].trim());
  const ready = provider
    ? Object.keys(changed).length > 0 &&
      !!label.trim() &&
      !!clientID.trim() &&
      whereReady
    : !!finalName &&
      !!clientID.trim() &&
      !!secret &&
      whereReady &&
      (!custom || !!label.trim());
  const flags = [
    !provider && (custom || finalName !== kind) && `--kind ${kind}`,
    ...(provider ? whereChanged : whereSet).map(
      (f) => `${whereFlag[f.key]} ${where[f.key].trim()}`,
    ),
    !provider &&
      fields
        .filter((f) => !f.optional && !where[f.key].trim())
        .map((f) => `${whereFlag[f.key]} <${f.key}>`)
        .join(" "),
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
      // 760px, a form page's width: every kind fits on one line.
      className="flex max-w-[760px] flex-col gap-3.5"
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
            ...Object.fromEntries(
              whereSet.map((f) => [f.key, where[f.key].trim()]),
            ),
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
        <div
          role="radiogroup"
          aria-label="Kind"
          className="flex flex-wrap gap-x-4 gap-y-2"
        >
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
                  setWhere(stored);
                }}
              />
              <ProviderMark kind={k.kind} />
              {k.label}
            </label>
          ))}
        </div>
      )}
      <p className="text-xs text-mut">
        {setupHint[kind]} Redirect URL:{" "}
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
      {fields.map((f) => (
        <Field
          key={f.key}
          label={f.label}
          note={f.optional ? "blank for gitlab.com" : undefined}
        >
          <input
            value={where[f.key]}
            onChange={(e) => setWhere({ ...where, [f.key]: e.target.value })}
            placeholder={f.placeholder}
            className="field font-mono"
          />
        </Field>
      ))}
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
