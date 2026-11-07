import { useSuspenseQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import type { components } from "../../api/schema";
import { useIsAdmin, useSession } from "../../data/queries";
import { CliHint } from "../../ui/CliHint";
import { day } from "../../ui/format";
import {
  Act,
  Button,
  Check,
  Confirm,
  CopyButton,
  ErrorText,
  Page,
  Panel,
  SectionHead,
} from "../../ui/kit";
import { Saved } from "../create/form";
import {
  orgQueries,
  useCreateSSOProvider,
  useDeleteSSOProvider,
  useSetOrgSSOOnly,
  useUpdateSSOProvider,
} from "./data";
import { OrgSettingsBar } from "./SettingsBar";

type Provider = components["schemas"]["SSOProvider"];
/** A provider's role for people it lets in without an invite; "" for none. */
type JITRole = NonNullable<Provider["jit_role"]> | "";

const jitRoles: Array<{ role: JITRole; label: string }> = [
  { role: "", label: "invite only" },
  { role: "read", label: "join as read" },
  { role: "deploy", label: "join as deploy" },
];

/** Where the identity provider sends the browser back to. */
const callbackPath = "/api/v1/auth/sso/callback";

/**
 * The organization's single sign-on (ADR 0058, `dock org sso`): its
 * OpenID Connect providers, the redirect URL to register at them, and
 * requiring SSO (`dock org sso-only`). Admins only: the API refuses
 * everyone else.
 */
export function SSOPage() {
  if (!useIsAdmin()) {
    return (
      <>
        <OrgSettingsBar />
        <Page>
          <p className="text-mut">
            Only the organization's admins manage single sign-on.
          </p>
        </Page>
      </>
    );
  }
  return <SSO />;
}

function SSO() {
  const providers = useSuspenseQuery(orgQueries.ssoProviders()).data;
  const [adding, setAdding] = useState(false);
  const org = useSuspenseQuery(orgQueries.org()).data;
  // The daemon's own address, as it tells providers; this page's before
  // there is one.
  const callback =
    providers[0]?.redirect_url ?? window.location.origin + callbackPath;
  return (
    <>
      <OrgSettingsBar
        actions={
          !adding && (
            <Button primary onClick={() => setAdding(true)}>
              Add provider
            </Button>
          )
        }
      />
      <Page>
        <dl className="props">
          <dt>Redirect URL</dt>
          <dd className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <code className="min-w-0 font-mono [overflow-wrap:anywhere]">
              {callback}
            </code>
            <CopyButton text={callback} />
            <span className="text-xs text-mut">
              Register it at the provider for a web app with scopes{" "}
              <span className="font-mono">openid email profile</span>.
            </span>
          </dd>
        </dl>
        {providers.length > 0 ? (
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Issuer</th>
                  <th>Client ID</th>
                  <th>Domains</th>
                  <th title="Who may join on their first sign-in">
                    New members
                  </th>
                  <th>Added</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {providers.map((p) => (
                  <ProviderRow
                    key={p.name}
                    provider={p}
                    last={org.sso_only && providers.length === 1}
                  />
                ))}
              </tbody>
            </table>
          </Panel>
        ) : (
          !adding && (
            <p className="text-mut">
              No providers yet. Add the one your team signs in with: Add
              provider.
            </p>
          )
        )}
        {adding && (
          <section
            aria-labelledby="add-provider"
            className="flex flex-col gap-4 rounded-panel border border-line p-4"
          >
            <h2 id="add-provider" className="text-md font-semibold">
              Add a provider
            </h2>
            <ProviderForm onDone={() => setAdding(false)} />
          </section>
        )}
        <SSOOnly
          on={org.sso_only}
          slug={org.slug}
          hasProviders={providers.length > 0}
        />
      </Page>
    </>
  );
}

/** A provider with Edit and Remove; editing opens its form in place. */
function ProviderRow({
  provider: p,
  last,
}: {
  provider: Provider;
  /** The org requires SSO and this is its only provider: it stays. */
  last: boolean;
}) {
  const remove = useDeleteSSOProvider();
  const [mode, setMode] = useState<"view" | "edit" | "remove">("view");
  const domains = p.allowed_domains ?? [];
  if (mode === "edit") {
    return (
      <tr>
        <td colSpan={7} className="py-3">
          <ProviderForm provider={p} onDone={() => setMode("view")} />
        </td>
      </tr>
    );
  }
  if (mode === "remove") {
    return (
      <tr>
        <td colSpan={7} className="py-2">
          <Confirm
            danger
            action={`Remove ${p.name}`}
            cli={`dock org sso remove ${p.name}`}
            pending={remove.isPending}
            error={remove.error}
            onConfirm={() =>
              remove.mutate(p.name, { onSuccess: () => setMode("view") })
            }
            onCancel={() => {
              remove.reset();
              setMode("view");
            }}
          >
            Remove <b className="font-semibold">{p.name}</b>?{" "}
            <span className="text-mut">
              {last
                ? "It's the only way in while single sign-on is required; turn that off first."
                : "People who signed in through it keep their accounts and memberships."}
            </span>
          </Confirm>
        </td>
      </tr>
    );
  }
  return (
    <tr>
      <td className="font-mono font-medium">{p.name}</td>
      <td className="max-w-[260px] truncate font-mono" title={p.issuer}>
        {p.issuer}
      </td>
      <td className="max-w-[200px] truncate font-mono" title={p.client_id}>
        {p.client_id}
      </td>
      <td className="font-mono">{domains.join(", ")}</td>
      <td className={p.jit_role ? undefined : "text-mut"}>
        {p.jit_role ? `join as ${p.jit_role}` : "invite only"}
      </td>
      <td className="text-mut" title={`by ${p.created_by}`}>
        {day(p.created_at)}
      </td>
      <td>
        <span className="flex justify-end gap-2">
          <Act
            small
            cli={`dock org sso set ${p.name}`}
            onClick={() => setMode("edit")}
          >
            Edit
          </Act>
          <Act
            danger
            small
            cli={`dock org sso remove ${p.name}`}
            onClick={() => setMode("remove")}
          >
            Remove
          </Act>
        </span>
      </td>
    </tr>
  );
}

/** Email domains typed as a list: commas or spaces, lowercase. */
const parseDomains = (text: string) =>
  text
    .split(/[\s,]+/)
    .map((d) => d.trim().toLowerCase())
    .filter(Boolean);

/**
 * Adding a provider, or changing one (`provider`): the client secret is
 * write-only, so a blank one keeps it, and only what changed is sent.
 */
function ProviderForm({
  provider,
  onDone,
}: {
  provider?: Provider;
  onDone?: () => void;
}) {
  const create = useCreateSSOProvider();
  const update = useUpdateSSOProvider();
  const action = provider ? update : create;
  const [name, setName] = useState("");
  const [issuer, setIssuer] = useState(provider?.issuer ?? "");
  const [clientID, setClientID] = useState(provider?.client_id ?? "");
  const [secret, setSecret] = useState("");
  const [domainText, setDomainText] = useState(
    (provider?.allowed_domains ?? []).join(", "),
  );
  const [jit, setJIT] = useState<JITRole>(provider?.jit_role ?? "");
  const domains = parseDomains(domainText);

  const changed = provider && {
    ...(issuer.trim() !== provider.issuer && { issuer: issuer.trim() }),
    ...(clientID.trim() !== provider.client_id && {
      client_id: clientID.trim(),
    }),
    ...(secret && { client_secret: secret }),
    ...(domains.join() !== (provider.allowed_domains ?? []).join() && {
      allowed_domains: domains,
    }),
    ...(jit !== (provider.jit_role ?? "") && { jit_role: jit }),
  };
  const flags = [
    (!provider || changed?.issuer) && `--issuer ${issuer.trim() || "<url>"}`,
    (!provider || changed?.client_id) &&
      `--client-id ${clientID.trim() || "<id>"}`,
    (!provider || changed?.client_secret) && "--client-secret-stdin",
    (!provider || changed?.allowed_domains) &&
      (domains.length ? domains : ["<domain>"])
        .map((d) => `--domain ${d}`)
        .join(" "),
    ((!provider && jit) || changed?.jit_role !== undefined) &&
      `--jit-role ${jit || '""'}`,
  ]
    .filter(Boolean)
    .join(" ");
  const cli = provider
    ? `dock org sso set ${provider.name} ${flags}`
    : `dock org sso add ${name.trim() || "<name>"} ${flags}`;
  const ready = provider
    ? Object.keys(changed ?? {}).length > 0 && domains.length > 0
    : !!name.trim() &&
      !!issuer.trim() &&
      !!clientID.trim() &&
      !!secret &&
      domains.length > 0;

  return (
    <form
      aria-label={provider ? `Change ${provider.name}` : "Add a provider"}
      className="flex flex-col gap-3 whitespace-normal"
      onSubmit={(e) => {
        e.preventDefault();
        if (!ready) return;
        if (provider) {
          update.mutate(
            { name: provider.name, ...changed },
            { onSuccess: () => onDone?.() },
          );
          return;
        }
        create.mutate(
          {
            name: name.trim(),
            issuer: issuer.trim(),
            client_id: clientID.trim(),
            client_secret: secret,
            allowed_domains: domains,
            ...(jit && { jit_role: jit }),
          },
          {
            onSuccess: () => {
              setName("");
              setIssuer("");
              setClientID("");
              setSecret("");
              setDomainText("");
              setJIT("");
              onDone?.();
            },
          },
        );
      }}
    >
      <div className="flex flex-wrap gap-3">
        {provider ? (
          <span className="flex flex-[1_1_140px] flex-col gap-1.5">
            <span>Name</span>
            <span className="py-[7px] font-mono font-medium">
              {provider.name}
            </span>
          </span>
        ) : (
          <Field
            label="Name"
            note="lowercase, digits, dashes"
            grow="flex-[1_1_140px]"
          >
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="google"
              className="field py-[7px] font-mono"
            />
          </Field>
        )}
        <Field label="Issuer URL" grow="flex-[1_1_320px]">
          <input
            type="url"
            value={issuer}
            onChange={(e) => setIssuer(e.target.value)}
            placeholder="https://accounts.google.com"
            className="field py-[7px] font-mono"
          />
        </Field>
      </div>
      <div className="flex flex-wrap gap-3">
        <Field label="Client ID" grow="flex-[1_1_260px]">
          <input
            value={clientID}
            onChange={(e) => setClientID(e.target.value)}
            className="field py-[7px] font-mono"
          />
        </Field>
        <Field
          label="Client secret"
          note={provider ? "blank keeps it" : "never shown again"}
          grow="flex-[1_1_260px]"
        >
          <input
            type="password"
            autoComplete="off"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            placeholder={provider ? "unchanged" : undefined}
            className="field py-[7px] font-mono"
          />
        </Field>
      </div>
      <div className="flex flex-wrap gap-3">
        <Field
          label="Email domains"
          note="it signs people in for"
          grow="flex-[1_1_320px]"
        >
          <input
            value={domainText}
            onChange={(e) => setDomainText(e.target.value)}
            placeholder="example.com, example.org"
            className="field py-[7px] font-mono"
          />
        </Field>
        <Field label="New members" grow="flex-[1_1_200px]">
          <select
            value={jit}
            onChange={(e) =>
              setJIT(
                jitRoles.find((r) => r.role === e.target.value)?.role ?? "",
              )
            }
            className="field py-[7px]"
          >
            {jitRoles.map((r) => (
              <option key={r.role} value={r.role}>
                {r.label}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <p className="text-xs text-mut">
        Invite only: people need an invite (
        <span className="font-mono">dock org invite</span>) before their first
        sign-in. Otherwise anyone with a verified email in these domains joins
        with that role; admins are always made by an admin.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <button
          type="submit"
          className="btn pri"
          disabled={!ready || action.isPending}
        >
          {provider ? "Save" : "Add provider"}
        </button>
        {onDone && (
          <Button
            onClick={() => {
              update.reset();
              onDone();
            }}
          >
            Cancel
          </Button>
        )}
        <span className="font-mono text-2xs text-mut [overflow-wrap:anywhere]">
          {cli}
        </span>
        <ErrorText error={action.error} />
      </div>
    </form>
  );
}

/** A labeled form field with a faint note after the label. */
function Field({
  label,
  note,
  grow,
  children,
}: {
  label: string;
  note?: string;
  /** Its flex class, like flex-[1_1_260px]. */
  grow: string;
  children: ReactNode;
}) {
  return (
    // biome-ignore lint/a11y/noLabelWithoutControl: the control is a child
    <label className={`flex min-w-0 flex-col gap-1.5 ${grow}`}>
      <span>
        {label} {note && <span className="text-xs text-faint">{note}</span>}
      </span>
      {children}
    </label>
  );
}

/**
 * Requiring single sign-on (`dock org sso-only`): the switch, who it
 * keeps out, the rule for turning it on, and the break-glass on the
 * server.
 */
function SSOOnly({
  on,
  slug,
  hasProviders,
}: {
  on: boolean;
  slug: string;
  hasProviders: boolean;
}) {
  const set = useSetOrgSSOOnly();
  const viaSSO = useSession()?.sso === slug;
  // Why it can't be turned on yet; the API says the same.
  const blocked = on
    ? undefined
    : !hasProviders
      ? "Add a provider first."
      : !viaSSO
        ? "Your own sign-in has to have gone through it first, so you know it works: sign out and sign in with SSO."
        : undefined;
  return (
    <section aria-labelledby="sso-only" className="flex flex-col gap-2">
      <SectionHead
        id="sso-only"
        aside={`dock org sso-only ${on ? "off" : "on"}`}
      >
        Require single sign-on
      </SectionHead>
      <span className="flex flex-col gap-1.5">
        <span className="flex flex-wrap items-center gap-2">
          <CliHint
            cli={`dock org sso-only ${on ? "off" : "on"}`}
            className="act self-start"
          >
            <Check
              label="Members act here only after signing in with SSO"
              checked={on}
              disabled={set.isPending || !!blocked}
              onChange={(sso_only) => set.mutate(sso_only)}
            />
          </CliHint>
          <Saved ok={set.isSuccess} />
        </span>
        <span className="text-xs text-mut">
          Sessions that signed in another way stay signed in but can't act in
          this organization until they sign in with SSO. API tokens are not
          affected.
        </span>
        {blocked && <span className="text-xs text-warn">{blocked}</span>}
        <span className="text-xs text-mut">
          Provider down? Someone with shell access to the server turns it off:{" "}
          <span className="font-mono text-fg">
            sudo dock admin sso-only {slug} off
          </span>
        </span>
        <ErrorText error={set.error} />
      </span>
    </section>
  );
}
