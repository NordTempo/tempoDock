import { lazy, Suspense, useEffect, useState } from "react";
import { api, body } from "../../api/client";
import {
  useAction,
  useGenerateDomain,
  useRemoveDomain,
} from "../../data/mutations";
import { domainNeedsAttention } from "../../data/problems";
import { listenPort, servicePath, useService } from "../../data/queries";
import type { Domain, ServiceRef } from "../../data/types";
import { ago, since } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Panel,
  SectionHead,
  StateText,
} from "../../ui/kit";
import { notify } from "../../ui/notice";
import { domainState } from "../../ui/status";
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
 * Add form, who can open the domains, a card per domain that needs a
 * human, and every domain. A domain added here is marked in the list; its
 * notice waits for Dock's first DNS check, so it says what the row says.
 */
export function DomainsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service, domains } = useService(ref);
  const port = listenPort(service);
  const [added, setAdded] = useState<string | null>(null);
  // A domain added here whose first DNS check hasn't come back yet.
  const [checking, setChecking] = useState<string | null>(null);
  const waiting = domains.filter(
    (d) => domainNeedsAttention(d) || d.verification || d.status === "added",
  );
  useEffect(() => {
    const d = checking && domains.find((x) => x.hostname === checking);
    if (!d || d.status === "added") return;
    notify(addedNotice(d, ref.service), d.status === "error" ? "fail" : "ok");
    setChecking(null);
  }, [checking, domains, ref.service]);

  return (
    <>
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
      <AccessLine serviceRef={ref} />
      {waiting.map((d) => (
        <DnsCard key={d.hostname} serviceRef={ref} domain={d} />
      ))}
      <section aria-labelledby="all-domains">
        <SectionHead id="all-domains">All domains</SectionHead>
        {domains.length === 0 ? (
          <p className="py-2 text-mut">
            No domains. The service is reachable on the private network at{" "}
            <span className="font-mono text-fg">
              {service.name}:{port}
            </span>
            .
          </p>
        ) : (
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Hostname</th>
                  <th>Status</th>
                  <th>Detail</th>
                  <th>Added</th>
                  <th />
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
                  />
                ))}
              </tbody>
            </table>
          </Panel>
        )}
      </section>
    </>
  );
}

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
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  return (
    <div className="flex flex-wrap items-center gap-2.5">
      <Button primary onClick={() => setOpen(true)}>
        Add domain
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
    </div>
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
 * The DNS records to create (the TXT record that proves control first,
 * while the domain isn't verified), what the name resolves to now, and
 * progress.
 */
function DnsCard({
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
      aria-labelledby={`dns-${domain.hostname}`}
      className={`flex flex-col gap-3.5 border p-4 ${error ? "border-fail-edge" : "border-warn-edge"}`}
    >
      <div className="flex flex-wrap items-baseline gap-x-3.5 gap-y-1.5">
        <h2
          id={`dns-${domain.hostname}`}
          className="font-mono text-[15px] font-medium"
        >
          {domain.hostname}
        </h2>
        <StateText state={domainState(domain)} />
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
            Check now
          </Act>
        </span>
      </div>
      <ErrorText error={check.error} />
      {domain.dns ? (
        <p
          className={`text-md ${domain.dns.status === "error" ? "text-fail" : ""}`}
        >
          {routedWords(domain.dns)}{" "}
          <span className="text-mut">Zone {domain.dns.zone}.</span>
          {error && domain.status_reason && (
            <span className="block text-fail">{domain.status_reason}</span>
          )}
        </p>
      ) : error ? (
        <p className="text-md text-fail">
          {domain.status_reason ?? "The certificate could not be issued."}
        </p>
      ) : proof ? (
        <p className="text-md">
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
        <p className="text-md">
          Create {records.length > 1 ? "one of these records" : "this record"}{" "}
          at your DNS provider. Nothing else is needed; the certificate is
          issued automatically once it resolves.
        </p>
      )}
      {records.length > 0 && !domain.dns && (
        <Panel>
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
        </Panel>
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
      <div className="flex flex-wrap gap-x-[22px] gap-y-1.5 text-xs">
        {steps.map((label, i) => (
          <span
            key={label}
            className={
              i + 1 === step ? (error ? "text-fail" : "text-warn") : "text-mut"
            }
          >
            {i + 1 === step ? (error ? "✕" : "▲") : i + 1 < step ? "✓" : "○"}{" "}
            {i + 1} {label}
          </span>
        ))}
      </div>
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
 * The records to create for a domain that doesn't resolve here yet, each
 * with its value to copy: "A store.example.com → 203.0.113.7".
 */
function RecordsToCreate({ domain: d }: { domain: Domain }) {
  const records = d.dns_records ?? [];
  return (
    <span className="flex flex-col gap-1 text-fg">
      {records.map((r, i) => (
        <span key={`${r.type} ${r.value}`} className="flex items-center gap-2">
          <span className="text-mut">{i === 0 ? "Create" : "or"}</span>
          <span className="font-mono">
            {r.type} {r.name} → {r.value}
          </span>
          <CopyButton text={r.value} />
        </span>
      ))}
    </span>
  );
}

/** A domain's row; `fresh` marks one just added here. */
function DomainRow({
  serviceRef: ref,
  domain: d,
  port,
  fresh,
}: {
  serviceRef: ServiceRef;
  domain: Domain;
  port: number;
  fresh: boolean;
}) {
  const remove = useRemoveDomain();
  const [confirming, setConfirming] = useState(false);
  // Until it resolves here, the row says which record makes it work.
  const needsRecord =
    !d.dns &&
    !d.verification &&
    (d.status === "added" || d.status === "waiting_for_dns") &&
    (d.dns_records?.length ?? 0) > 0;
  return (
    <tr className={fresh ? "[&>td]:bg-sel" : ""}>
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
        {needsRecord ? (
          <RecordsToCreate domain={d} />
        ) : (
          <>
            {detail(d)}
            {d.redirect_to
              ? ` · redirects to ${d.redirect_to}`
              : ` · to :${port}`}
            <span className="text-faint">{lastCheck(d)}</span>
          </>
        )}
      </td>
      <td className="text-mut">{fresh ? "just added" : since(d.created_at)}</td>
      <td className="py-1">
        <Button small onClick={() => setConfirming(true)}>
          Remove
        </Button>
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
            Requests to <b>{d.hostname}</b> stop reaching {ref.service}. You can
            add it again later.
          </Confirm>
        )}
      </td>
    </tr>
  );
}
