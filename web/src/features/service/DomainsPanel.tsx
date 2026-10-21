import { useState } from "react";
import {
  useAddDomain,
  useGenerateDomain,
  useRemoveDomain,
} from "../../data/mutations";
import { domainNeedsAttention } from "../../data/problems";
import { useService } from "../../data/queries";
import type { Domain, ServiceRef } from "../../data/types";
import { ago, since } from "../../ui/format";
import {
  Button,
  CopyButton,
  ErrorText,
  Scroll,
  SectionHead,
  StateText,
} from "../../ui/kit";
import { domainState } from "../../ui/status";

/** Add form, a card per domain that needs a human, and every domain. */
export function DomainsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service, domains } = useService(ref);
  const port = service.port || 8080;
  const waiting = domains.filter(domainNeedsAttention);

  return (
    <>
      <AddDomain
        serviceRef={ref}
        hasGenerated={domains.some((d) => d.generated)}
      />
      {waiting.map((d) => (
        <DnsCard key={d.hostname} domain={d} />
      ))}
      <section aria-labelledby="all-domains">
        <SectionHead id="all-domains">All domains</SectionHead>
        {domains.length === 0 ? (
          <p className="py-2 text-mut">
            No domains. The service is reachable on the private network at{" "}
            <span className="font-mono text-white">
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
                  />
                ))}
              </tbody>
            </table>
          </Scroll>
        )}
      </section>
    </>
  );
}

function AddDomain({
  serviceRef: ref,
  hasGenerated,
}: {
  serviceRef: ServiceRef;
  hasGenerated: boolean;
}) {
  const add = useAddDomain();
  const generate = useGenerateDomain();
  const [hostname, setHostname] = useState("");
  return (
    <form
      aria-label="Add domain"
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(
          { ref, hostname: hostname.trim() },
          { onSuccess: () => setHostname("") },
        );
      }}
    >
      <label htmlFor="hostname" className="text-mut">
        Add a hostname
      </label>
      <input
        id="hostname"
        value={hostname}
        onChange={(e) => setHostname(e.target.value)}
        placeholder="app.example.com"
        className="field min-w-0 flex-[1_1_320px] py-[7px] font-mono"
      />
      <button
        type="submit"
        className="btn pri"
        disabled={add.isPending || !hostname.trim()}
      >
        Add domain
      </button>
      {!hasGenerated && (
        <Button
          disabled={generate.isPending}
          onClick={() => generate.mutate(ref)}
        >
          Generate one
        </Button>
      )}
      <span className="font-mono text-2xs text-mut">
        dock domain add {"<host>"} -s {ref.service} -e {ref.environment}
      </span>
      <ErrorText error={add.error ?? generate.error} />
    </form>
  );
}

/** The DNS record to create, what the name resolves to now, and progress. */
function DnsCard({ domain }: { domain: Domain }) {
  const records = domain.dns_records ?? [];
  const resolved = domain.resolved_ips ?? [];
  const error = domain.status === "error";
  const step =
    domain.status === "waiting_for_dns" || domain.status === "added"
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
        {domain.checked_at && (
          <span className="ml-auto text-mut">
            Checked {ago(domain.checked_at)}
          </span>
        )}
      </div>
      {error ? (
        <p className="text-md text-fail">
          {domain.status_reason ?? "The certificate could not be issued."}
        </p>
      ) : (
        <p className="text-md">
          Create {records.length > 1 ? "one of these records" : "this record"}{" "}
          at your DNS provider. Nothing else is needed; the certificate is
          issued automatically once it resolves.
        </p>
      )}
      {records.length > 0 && (
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
      <div className="flex flex-wrap gap-x-[22px] gap-y-1.5 text-xs">
        {["Waiting for DNS", "Issuing certificate", "Active"].map(
          (label, i) => (
            <span
              key={label}
              className={
                i + 1 === step
                  ? error
                    ? "text-fail"
                    : "text-warn"
                  : "text-mut"
              }
            >
              {i + 1 === step ? (error ? "✕" : "▲") : i + 1 < step ? "✓" : "○"}{" "}
              {i + 1} {label}
            </span>
          ),
        )}
      </div>
    </section>
  );
}

function detail(d: Domain) {
  switch (d.status) {
    case "active":
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

function DomainRow({
  serviceRef: ref,
  domain: d,
  port,
}: {
  serviceRef: ServiceRef;
  domain: Domain;
  port: number;
}) {
  const remove = useRemoveDomain();
  const [confirming, setConfirming] = useState(false);
  return (
    <tr>
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
        {detail(d)}
        {d.redirect_to ? ` · redirects to ${d.redirect_to}` : ` · to :${port}`}
      </td>
      <td className="text-mut">{since(d.created_at)}</td>
      <td className="py-1">
        {confirming ? (
          <span className="flex items-center gap-2">
            <span className="text-mut">Traffic to it stops.</span>
            <Button
              small
              danger
              disabled={remove.isPending}
              onClick={() => remove.mutate({ ref, hostname: d.hostname })}
            >
              Remove
            </Button>
            <Button small onClick={() => setConfirming(false)}>
              Cancel
            </Button>
          </span>
        ) : (
          <Button small onClick={() => setConfirming(true)}>
            Remove
          </Button>
        )}
        <ErrorText error={remove.error} />
      </td>
    </tr>
  );
}
