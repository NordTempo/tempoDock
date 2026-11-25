import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { lazy, Suspense, useEffect, useState } from "react";
import { api, body } from "../../api/client";
import {
  useAction,
  useGenerateDomain,
  useRemoveDomain,
} from "../../data/mutations";
import { domainNeedsAttention } from "../../data/problems";
import {
  listenPort,
  servicePath,
  useCanDeploy,
  useService,
} from "../../data/queries";
import type { Domain, ServiceRef } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  MoreItem,
  MoreMenu,
  MoreSeparator,
  Scroll,
  StateText,
} from "../../ui/kit";
import { notify } from "../../ui/notice";
import { SettingsSection } from "../../ui/sections";
import { domainState } from "../../ui/status";
import { useDisableHostname, useEnableHostname } from "../analytics/data";
import { sitesQuery } from "../analytics/loaders";
import { AccessLine } from "./AccessLine";

// Loads when it opens.
const AddDomainDialog = lazy(() => import("./AddDomainDialog"));

/**
 * What a notice says about a domain just added, in the words its row
 * uses: serving, routed through Cloudflare, or the record still to create.
 */
function addedNotice(d: Domain, service: string) {
  const record = d.dns_records?.[0];
  if (d.status === "active")
    return `Added ${d.hostname}: it serves ${service} now`;
  if (d.dns)
    return `Added ${d.hostname}: Dock is routing it through Cloudflare`;
  if (d.verification)
    return `Added ${d.hostname}: create the TXT record shown below to verify it`;
  if (d.status === "error")
    return `Added ${d.hostname}, but ${d.status_reason ?? "it can't be served yet"}`;
  if (d.status === "issuing_certificate")
    return `Added ${d.hostname}: DNS ok, issuing its certificate`;
  return `Added ${d.hostname}: create ${record ? `${record.type} ${record.value}` : "its DNS record"} at your DNS provider`;
}

/**
 * Settings' Domains section: who can open them, then one table of the
 * service's domains (where each goes and whether Dock Analytics counts
 * its visits). A domain still waiting for DNS, verification or its
 * certificate opens its records and "Check again" under its row. A domain
 * added here is marked in the list; its notice waits for Dock's first DNS
 * check, so it says what the row says.
 */
export function DomainsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const { service, domains } = useService(ref);
  const port = listenPort(service);
  const [added, setAdded] = useState<string | null>(null);
  // A domain added here whose first DNS check hasn't come back yet.
  const [checking, setChecking] = useState<string | null>(null);
  const waiting = domains.filter(
    (d) => domainNeedsAttention(d) || d.verification || d.status === "added",
  );
  // Which hostnames Dock Analytics counts, and for which site.
  const { data: sites } = useSuspenseQuery(sitesQuery(ref.project));
  const counted = new Map(
    sites
      .filter((s) => !s.archived && s.environment === ref.environment)
      .flatMap((s) =>
        (s.hostnames ?? [])
          .filter((h) => h.service === ref.service)
          .map((h) => [h.hostname, s.name] as const),
      ),
  );
  useEffect(() => {
    const d = checking && domains.find((x) => x.hostname === checking);
    if (!d || d.status === "added") return;
    notify(addedNotice(d, ref.service), d.status === "error" ? "fail" : "ok");
    setChecking(null);
  }, [checking, domains, ref.service]);

  return (
    <SettingsSection
      id="domains"
      title="Domains"
      actions={
        <AddDomain
          serviceRef={ref}
          hasGenerated={domains.some((d) => d.generated)}
          onAdded={(d) => {
            setAdded(d.hostname);
            if (d.status === "added" && !d.dns && !d.verification)
              setChecking(d.hostname);
            else
              notify(
                addedNotice(d, ref.service),
                d.status === "error" ? "fail" : "ok",
              );
          }}
        />
      }
    >
      <div className="flex flex-col gap-3">
        <AccessLine serviceRef={ref} />
        {domains.length === 0 ? (
          <p className="py-2 text-mut">
            No domains. Other services reach it on the private network at{" "}
            <span className="font-mono text-fg">
              {service.name}:{port}
            </span>
            .
          </p>
        ) : (
          <Scroll>
            <table>
              <thead>
                <tr>
                  <th>Hostname</th>
                  <th>Status</th>
                  <th>Goes to</th>
                  <th>Visits</th>
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {domains.map((d) => (
                  <DomainRow
                    key={d.hostname}
                    serviceRef={ref}
                    domain={d}
                    port={port}
                    fresh={d.hostname === added}
                    waiting={waiting.includes(d)}
                    site={counted.get(d.hostname)}
                  />
                ))}
              </tbody>
            </table>
          </Scroll>
        )}
        {counted.size > 0 && (
          <p className="flex flex-wrap items-center gap-2 text-xs text-mut">
            Counted pages load the script, in their {"<head>"}:
            <code className="font-mono text-fg">{scriptTag}</code>
            <CopyButton text={scriptTag} />
            <Link to="/$org/analytics" params={{ org }} className="lnk ml-2">
              Analytics
            </Link>
          </p>
        )}
      </div>
    </SettingsSection>
  );
}

/** The script a counted hostname's pages load (ADR 0037). */
const scriptTag = '<script src="/_dock/a/s.js" defer></script>';

/** The section's Add domain (N), and Generate one while it has no generated domain. */
function AddDomain({
  serviceRef: ref,
  hasGenerated,
  onAdded,
}: {
  serviceRef: ServiceRef;
  hasGenerated: boolean;
  onAdded: (domain: Domain) => void;
}) {
  const generate = useGenerateDomain();
  const [open, setOpen] = useState(false);
  useShortcuts([{ key: "n", label: "add domain", run: () => setOpen(true) }]);
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  return (
    <>
      <Button onClick={() => setOpen(true)}>
        Add domain <kbd>N</kbd>
      </Button>
      {!hasGenerated && (
        <Act
          cli={`dock domain generate ${flags}`}
          disabled={generate.isPending}
          onClick={() => generate.mutate(ref)}
        >
          Generate one
        </Act>
      )}
      <ErrorText error={generate.error} />
      {open && (
        <Suspense fallback={null}>
          <AddDomainDialog
            serviceRef={ref}
            onClose={() => setOpen(false)}
            onAdded={onAdded}
          />
        </Suspense>
      )}
    </>
  );
}

/** How Dock routes a domain added from Cloudflare, in words. */
function routedWords(dns: NonNullable<Domain["dns"]>) {
  switch (dns.status) {
    case "pending":
      return "Dock is setting up its route through the server's Cloudflare tunnel.";
    case "ready":
      return "Dock routed it through the server's Cloudflare tunnel.";
    case "external":
      return "Its CNAME, made outside Dock, already points at the server's tunnel.";
    case "error":
      return `Dock couldn't route it through the server's tunnel: ${dns.reason ?? "an operator can retry in Console › Cloudflare"}`;
    case "deleting":
      return "Dock is removing its route.";
  }
}

/**
 * Checks a domain now instead of at the server's next retry. The domain
 * event it publishes updates the page.
 */
const useCheckDomain = () =>
  useAction(
    ({ ref, hostname }: { ref: ServiceRef; hostname: string }) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/domains/{hostname}/check",
          { params: { path: { ...servicePath(ref), hostname } } },
        ),
      ),
    () => [],
  );

/**
 * Under a domain's row while it isn't serving yet: the DNS records to
 * create (the TXT record that proves control first, while the domain
 * isn't verified), what the name resolves to now, progress, and Check
 * again, which asks now instead of at the server's next retry.
 */
function DnsSteps({
  serviceRef: ref,
  domain,
}: {
  serviceRef: ServiceRef;
  domain: Domain;
}) {
  const check = useCheckDomain();
  const proof = domain.verification;
  const records = [...(proof ? [proof] : []), ...(domain.dns_records ?? [])];
  const resolved = domain.resolved_ips ?? [];
  const error = domain.status === "error";
  const steps = [
    ...(proof ? ["Verify"] : []),
    "Waiting for DNS",
    "Issuing certificate",
    "Active",
  ];
  const step = proof
    ? 1
    : domain.status === "waiting_for_dns" || domain.status === "added"
      ? 1
      : domain.status === "issuing_certificate"
        ? 2
        : 3;
  return (
    <section
      aria-label={`What ${domain.hostname} still needs`}
      className="flex flex-col gap-3 py-1.5"
    >
      <div className="flex flex-wrap items-baseline gap-x-3.5 gap-y-1.5">
        {/* Steps, not counts: joined by arrows, only the current one coloured. */}
        <ol
          aria-label="Progress"
          className="flex flex-wrap items-center gap-x-2 gap-y-1.5 text-xs"
        >
          {steps.map((label, i) => (
            <li key={label} className="flex items-center gap-2">
              {i > 0 && (
                <span aria-hidden className="text-faint">
                  →
                </span>
              )}
              <span
                aria-current={i + 1 === step ? "step" : undefined}
                className={
                  i + 1 === step
                    ? error
                      ? "text-fail"
                      : "text-warn"
                    : i + 1 < step
                      ? "text-mut"
                      : "text-faint"
                }
              >
                {label}
              </span>
            </li>
          ))}
        </ol>
        <span className="ml-auto flex items-center gap-2.5">
          {domain.checked_at && (
            <span className="text-mut">Checked {ago(domain.checked_at)}</span>
          )}
          <Act
            small
            cli={`dock domain check ${domain.hostname} -s ${ref.service} -e ${ref.environment}`}
            disabled={check.isPending}
            onClick={() => check.mutate({ ref, hostname: domain.hostname })}
          >
            Check again
          </Act>
        </span>
      </div>
      <ErrorText error={check.error} />
      {domain.dns ? (
        <p className={domain.dns.status === "error" ? "text-fail" : ""}>
          {routedWords(domain.dns)}{" "}
          <span className="text-mut">Zone {domain.dns.zone}.</span>
          {error && domain.status_reason && (
            <span className="block text-fail">{domain.status_reason}</span>
          )}
        </p>
      ) : error ? (
        <p className="text-fail">
          {domain.status_reason ?? "The certificate could not be issued."}
        </p>
      ) : proof ? (
        <p>
          Create the TXT record to prove you control this hostname
          {records.length > 2
            ? ", and one of the others to point it here"
            : records.length > 1
              ? ", and the other to point it here"
              : ""}
          . Dock routes it once it finds the TXT record.
          {domain.status_reason && (
            <span className="block text-mut">{domain.status_reason}</span>
          )}
        </p>
      ) : (
        <p>
          Create {records.length > 1 ? "one of these records" : "this record"}{" "}
          at your DNS provider. Nothing else is needed; the certificate is
          issued automatically once it resolves.
        </p>
      )}
      {records.length > 0 && !domain.dns && (
        <Scroll>
          <table className="font-mono">
            <thead>
              <tr>
                <th>Type</th>
                <th>Name</th>
                <th>Value</th>
              </tr>
            </thead>
            <tbody>
              {records.map((r) => (
                <tr key={`${r.type} ${r.value}`}>
                  <td>{r.type}</td>
                  <td>
                    {r.name} <CopyButton text={r.name} />
                  </td>
                  <td className="w-full">
                    {r.value} <CopyButton text={r.value} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Scroll>
      )}
      {domain.checked_at && (
        <div className="flex flex-wrap gap-x-6 gap-y-1">
          <span className="text-mut">Resolves now to</span>
          <span className="font-mono">
            {resolved.length ? (
              <>
                <span className="text-fail">✕</span> {resolved.join(", ")}
              </>
            ) : (
              <span className="text-mut">nothing yet</span>
            )}
          </span>
          {!error && domain.status_reason && (
            <span className="text-mut">{domain.status_reason}</span>
          )}
        </div>
      )}
    </section>
  );
}

/**
 * When the server last looked the hostname up and what it found, as
 * `dock domain list --wide` shows it.
 */
function lastCheck(d: Domain) {
  if (!d.checked_at) return "";
  const ips = d.resolved_ips ?? [];
  return ` · checked ${ago(d.checked_at)}, resolved to ${ips.length ? ips.join(", ") : "nothing"}`;
}

function detail(d: Domain) {
  if (d.dns && d.status !== "active") {
    if (d.dns.status === "error")
      return `Cloudflare: ${d.dns.reason ?? "error"}`;
    if (d.status === "error") return d.status_reason ?? "error";
    return `through the tunnel · Cloudflare ${d.dns.status}`;
  }
  if (d.verification && d.status !== "error") {
    return `not routed · needs TXT ${d.verification.name}`;
  }
  switch (d.status) {
    case "active":
      if (d.dns) return "through the server's Cloudflare tunnel";
      return d.generated
        ? "generated · certificate issued"
        : "certificate issued";
    case "issuing_certificate":
      return "DNS ok · issuing the certificate";
    case "added":
      return "checking DNS";
    case "waiting_for_dns": {
      const record = d.dns_records?.[0];
      const ips = d.resolved_ips ?? [];
      return `needs ${record ? `${record.type} ${record.value}` : "a DNS record"}${ips.length ? `, resolves to ${ips.join(", ")}` : ""}`;
    }
    case "error":
      return d.status_reason ?? "error";
  }
}

/**
 * A domain's row: hostname, state, where it goes, whether its visits are
 * counted (with Count visits when not), and its menu (Stop counting,
 * Remove). `fresh` marks one just added here; `waiting` opens what it
 * still needs (DnsSteps) under it.
 */
function DomainRow({
  serviceRef: ref,
  domain: d,
  port,
  fresh,
  waiting,
  site,
}: {
  serviceRef: ServiceRef;
  domain: Domain;
  port: number;
  fresh: boolean;
  waiting: boolean;
  /** The analytics site that counts its visits, if one does. */
  site?: string;
}) {
  const org = useOrg();
  const remove = useRemoveDomain();
  const canDeploy = useCanDeploy(ref);
  const enable = useEnableHostname();
  const disable = useDisableHostname();
  const [confirming, setConfirming] = useState(false);
  return (
    <>
      <tr
        className={`${fresh ? "[&>td]:bg-sel" : ""} ${waiting ? "[&>td]:border-b-transparent" : ""}`}
      >
        <td className="font-mono">
          <a
            href={d.url}
            target="_blank"
            rel="noreferrer"
            className="no-underline hover:underline"
          >
            {d.hostname}
          </a>
        </td>
        <td>
          <StateText state={domainState(d)} />
        </td>
        <td
          className={`w-full whitespace-normal ${d.status === "error" ? "text-fail" : "text-mut"}`}
        >
          {d.redirect_to ? `redirects to ${d.redirect_to}` : `:${port}`}
          {waiting ? "" : ` · ${detail(d)}`}
          <span className="text-faint">{lastCheck(d)}</span>
        </td>
        <td className="whitespace-nowrap">
          {d.redirect_to ? (
            <span className="text-faint">-</span>
          ) : site ? (
            <Link
              to="/$org/analytics/$project/$site"
              params={{ org, project: ref.project, site }}
              title={`Counted for site ${site}`}
            >
              counted
            </Link>
          ) : (
            <Act
              small
              cli={`dock analytics enable ${d.hostname}`}
              disabled={!canDeploy || enable.isPending}
              onClick={() => enable.mutate({ hostname: d.hostname })}
            >
              Count visits
            </Act>
          )}
        </td>
        <td className="py-1 text-right">
          <MoreMenu label={`${d.hostname} actions`}>
            {(close) => (
              <>
                {site && (
                  <MoreItem
                    close={close}
                    cli={`dock analytics disable ${d.hostname}`}
                    disabled={!canDeploy || disable.isPending}
                    onClick={() => disable.mutate(d.hostname)}
                  >
                    Stop counting visits
                  </MoreItem>
                )}
                {site && <MoreSeparator />}
                <MoreItem
                  close={close}
                  danger
                  cli={`dock domain remove ${d.hostname} -s ${ref.service} -e ${ref.environment}`}
                  onClick={() => setConfirming(true)}
                >
                  Remove {d.hostname}…
                </MoreItem>
              </>
            )}
          </MoreMenu>
          <ErrorText error={enable.error ?? disable.error} />
          {confirming && (
            <Confirm
              danger
              action={`Remove ${d.hostname}`}
              cli={`dock domain remove ${d.hostname} -s ${ref.service} -e ${ref.environment}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() =>
                remove.mutate(
                  { ref, hostname: d.hostname },
                  { onSuccess: () => setConfirming(false) },
                )
              }
              onCancel={() => {
                remove.reset();
                setConfirming(false);
              }}
            >
              Requests to <b>{d.hostname}</b> stop reaching {ref.service}. You
              can add it again later.
            </Confirm>
          )}
        </td>
      </tr>
      {waiting && (
        <tr>
          <td colSpan={5} className="whitespace-normal">
            <DnsSteps serviceRef={ref} domain={d} />
          </td>
        </tr>
      )}
    </>
  );
}
