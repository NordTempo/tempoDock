import { useState } from "react";
import {
  useAddDomain,
  useGenerateDomain,
  useRemoveDomain,
} from "../../data/mutations";
import { useDomains, useService } from "../../data/queries";
import type { Domain } from "../../data/types";
import { since } from "../../ui/format";
import {
  Action,
  Button,
  ErrorText,
  Heading,
  inputClass,
  Status,
} from "../../ui/kit";
import { domainStatus } from "../../ui/status";

/** Domains with TLS state, and the exact DNS record for any still waiting. */
export function DomainsPanel({ serviceId }: { serviceId: string }) {
  const service = useService(serviceId);
  const domains = useDomains(serviceId);
  const generate = useGenerateDomain();
  const [adding, setAdding] = useState(false);
  const waiting = domains.filter((d) => d.status.state === "waiting_dns");

  return (
    <div className="px-5">
      <Heading
        note={`port ${service.settings.port}`}
        actions={
          <>
            <Action
              disabled={generate.isPending}
              onClick={() => generate.mutate(serviceId)}
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
      {adding && (
        <AddDomain serviceId={serviceId} onDone={() => setAdding(false)} />
      )}

      {domains.length === 0 ? (
        <p className="py-2 text-dim">
          No domains. The service is reachable on the private network at{" "}
          <span className="font-mono text-xs">{service.privateAddress}</span>.
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
              <DomainRow key={domain.id} domain={domain} />
            ))}
          </tbody>
        </table>
      )}

      {waiting.map((domain) => (
        <DnsInstructions key={domain.id} domain={domain} />
      ))}
    </div>
  );
}

function certificate(domain: Domain) {
  switch (domain.status.state) {
    case "active":
      return domain.status.certificate;
    case "issuing_certificate":
      return "issuing";
    case "waiting_dns":
      return "not issued";
    case "error":
      return domain.status.message;
  }
}

function DomainRow({ domain }: { domain: Domain }) {
  const remove = useRemoveDomain();
  const [confirming, setConfirming] = useState(false);
  return (
    <tr>
      <td>{domain.hostname}</td>
      <td>
        <Status {...domainStatus(domain.status)} />
      </td>
      <td className={domain.status.state === "error" ? "text-bad" : "text-dim"}>
        {certificate(domain)}
      </td>
      <td className="font-mono text-xs text-dim">:{domain.targetPort}</td>
      <td className="w-px text-right">
        {confirming ? (
          <span className="flex justify-end gap-3">
            <span className="text-dim">Traffic to this host stops.</span>
            <button
              type="button"
              className="text-bad"
              disabled={remove.isPending}
              onClick={() =>
                remove.mutate({
                  serviceId: domain.serviceId,
                  domainId: domain.id,
                })
              }
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
  if (domain.status.state !== "waiting_dns") return null;
  const { record, resolvesTo, checkedAt } = domain.status;
  return (
    <div className="pt-1.5">
      <div className="grid max-w-[640px] grid-cols-[60px_1fr_1fr_70px] gap-x-3 border border-line2 px-2.5 py-2 font-mono text-xs">
        <span className="text-dim">Type</span>
        <span className="text-dim">Name</span>
        <span className="text-dim">Value</span>
        <span />
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
      <p className="pt-0.5 text-xs text-dim">
        {resolvesTo
          ? `Currently resolves to ${resolvesTo}.`
          : "Currently resolves to nothing (NXDOMAIN)."}{" "}
        Checked{" "}
        {since(checkedAt) === "now" ? "just now" : `${since(checkedAt)} ago`}.
        Certificate is issued on the first request after DNS points here.
      </p>
    </div>
  );
}

function AddDomain({
  serviceId,
  onDone,
}: {
  serviceId: string;
  onDone: () => void;
}) {
  const add = useAddDomain();
  const [hostname, setHostname] = useState("");
  return (
    <form
      className="flex items-baseline gap-3 py-2"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate({ serviceId, hostname }, { onSuccess: onDone });
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
