import { useState } from "react";
import {
  useAddDomain,
  useGenerateDomain,
  useRemoveDomain,
} from "../../data/mutations";
import { useService } from "../../data/queries";
import type { Domain, ServiceRef } from "../../data/types";
import {
  Action,
  Button,
  ErrorText,
  Heading,
  inputClass,
  Status,
} from "../../ui/kit";
import { domainStatus } from "../../ui/status";

/** Domains with TLS state, and the exact DNS records for any still waiting. */
export function DomainsPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service, domains } = useService(ref);
  const generate = useGenerateDomain();
  const [adding, setAdding] = useState(false);
  const port = service.port || 8080;
  // Generated domains are covered by the wildcard record.
  const waiting = domains.filter(
    (d) =>
      (d.status === "waiting_for_dns" || d.status === "added") &&
      (d.dns_records ?? []).length > 0,
  );

  return (
    <div className="px-5">
      <Heading
        note={`port ${port}`}
        actions={
          <>
            <Action
              disabled={generate.isPending}
              onClick={() => generate.mutate(ref)}
            >
              Generate
            </Action>
            <button
              type="button"
              className="hover:underline"
              onClick={() => setAdding(true)}
            >
              Add domain
            </button>
          </>
        }
      >
        Domains
      </Heading>
      <ErrorText error={generate.error} />
      {adding && <AddDomain serviceRef={ref} onDone={() => setAdding(false)} />}

      {domains.length === 0 ? (
        <p className="py-2 text-dim">
          No domains. The service is reachable on the private network at{" "}
          <span className="font-mono text-xs">
            {service.name}:{port}
          </span>
          .
        </p>
      ) : (
        <table className="tbl">
          <thead>
            <tr>
              <th>Hostname</th>
              <th>Status</th>
              <th>Certificate</th>
              <th>Target</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {domains.map((domain) => (
              <DomainRow
                key={domain.hostname}
                serviceRef={ref}
                domain={domain}
                port={port}
              />
            ))}
          </tbody>
        </table>
      )}

      {waiting.map((domain) => (
        <DnsInstructions key={domain.hostname} domain={domain} />
      ))}
    </div>
  );
}

function certificate(domain: Domain) {
  switch (domain.status) {
    case "active":
      return "issued";
    case "issuing_certificate":
      return "issuing";
    case "added":
    case "waiting_for_dns":
      return "not issued";
    case "error":
      return domain.status_reason ?? "error";
  }
}

function DomainRow({
  serviceRef: ref,
  domain,
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
      <td>
        {domain.hostname}
        {domain.generated && <span className="text-dim"> generated</span>}
      </td>
      <td>
        <Status {...domainStatus(domain)} />
      </td>
      <td className={domain.status === "error" ? "text-bad" : "text-dim"}>
        {certificate(domain)}
      </td>
      <td className="font-mono text-xs text-dim">
        {domain.redirect_to ? `→ ${domain.redirect_to}` : `:${port}`}
      </td>
      <td className="w-px text-right">
        {confirming ? (
          <span className="flex justify-end gap-3">
            <span className="text-dim">Traffic to this host stops.</span>
            <button
              type="button"
              className="text-bad"
              disabled={remove.isPending}
              onClick={() => remove.mutate({ ref, hostname: domain.hostname })}
            >
              Remove
            </button>
            <Action onClick={() => setConfirming(false)}>Cancel</Action>
          </span>
        ) : (
          <Action onClick={() => setConfirming(true)}>remove</Action>
        )}
        <ErrorText error={remove.error} />
      </td>
    </tr>
  );
}

function DnsInstructions({ domain }: { domain: Domain }) {
  const records = domain.dns_records ?? [];
  return (
    <div className="pt-1.5">
      <p className="pb-1 text-xs">
        {domain.hostname}
        {domain.status_reason && (
          <span className="text-dim"> · {domain.status_reason}</span>
        )}
      </p>
      <div className="grid max-w-[640px] grid-cols-[60px_1fr_1fr_70px] gap-x-3 border border-line2 px-2.5 py-2 font-mono text-xs">
        <span className="text-dim">Type</span>
        <span className="text-dim">Name</span>
        <span className="text-dim">Value</span>
        <span />
        {records.map((record) => (
          <div key={`${record.type} ${record.value}`} className="contents">
            <span>{record.type}</span>
            <span>{record.name}</span>
            <span>{record.value}</span>
            <Action
              className="text-left"
              onClick={() => navigator.clipboard.writeText(record.value)}
            >
              copy
            </Action>
          </div>
        ))}
      </div>
      <p className="pt-0.5 text-xs text-dim">
        {records.length > 1 ? "Any one of these is enough. " : ""}
        The certificate is issued once DNS points here.
      </p>
    </div>
  );
}

function AddDomain({
  serviceRef: ref,
  onDone,
}: {
  serviceRef: ServiceRef;
  onDone: () => void;
}) {
  const add = useAddDomain();
  const [hostname, setHostname] = useState("");
  return (
    <form
      className="flex items-baseline gap-3 py-2"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate({ ref, hostname: hostname.trim() }, { onSuccess: onDone });
      }}
    >
      <input
        // biome-ignore lint/a11y/noAutofocus: opened on request
        autoFocus
        value={hostname}
        onChange={(e) => setHostname(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && onDone()}
        placeholder="shop.example.com"
        aria-label="Hostname"
        className={`${inputClass} w-72`}
      />
      <Button primary type="submit" disabled={add.isPending}>
        Add
      </Button>
      <Action onClick={onDone}>Cancel</Action>
      <ErrorText error={add.error} />
    </form>
  );
}
