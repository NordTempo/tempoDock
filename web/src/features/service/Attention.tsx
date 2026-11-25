import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { domainNeedsAttention } from "../../data/problems";
import type { Domain, ServiceRef, ServiceView } from "../../data/types";
import { useOrg } from "../../shell/location";
import { VulnRow } from "../images/VulnRow";

/**
 * What needs a look on a service, said once (ADR 0076): under the
 * Overview's status, one list instead of stacked bands. A row each for
 * critical or high vulnerabilities in the live image, a down uptime check
 * and a domain that needs attention, each with its one way to act (a
 * failed deploy has its own block above, FailedDeploy). Elsewhere these are a dot, a chip or a count.
 * Nothing renders while nothing needs a look.
 */
export function NeedsAttention({
  serviceRef: ref,
  view,
}: {
  serviceRef: ServiceRef;
  view: ServiceView;
}) {
  const org = useOrg();
  const scan = view.scan;
  const vulnerable =
    scan?.state === "scanned" && (scan.critical > 0 || scan.high > 0);
  const down = view.uptime?.state === "down";
  const domains = view.domains.filter(domainNeedsAttention);
  if (!vulnerable && !down && !domains.length) return null;
  return (
    <section
      aria-label="Needs attention"
      className="border-t border-line [&>*:last-child]:border-b-0"
    >
      {vulnerable && scan && (
        <VulnRow serviceRef={ref} view={view} scan={scan} />
      )}
      {down && (
        <Row
          tone="fail"
          action={
            <Link
              to="/$org/$project/$environment/$service/settings"
              params={{ org, ...ref }}
              hash="health"
              className="btn sm"
            >
              Uptime checks
            </Link>
          }
        >
          <span className="text-fail">An uptime check is down</span>
        </Row>
      )}
      {domains[0] && (
        <Row
          tone="warn"
          action={
            <Link
              to="/$org/$project/$environment/$service/settings"
              params={{ org, ...ref }}
              hash="domains"
              className="btn sm"
            >
              {domains[0].status === "waiting_for_dns"
                ? "Set up DNS"
                : domains.length > 1
                  ? "Fix domains"
                  : "Fix domain"}
            </Link>
          }
        >
          <span className="font-mono">{domains[0].hostname}</span>{" "}
          {domainProblem(domains[0])}
          {domains.length > 1 && (
            <span className="text-mut">
              {" "}
              · {domains.length - 1} more{" "}
              {domains.length > 2 ? "domains need" : "domain needs"} attention
            </span>
          )}
        </Row>
      )}
    </section>
  );
}

/**
 * What is wrong with a domain that needs attention, after its hostname:
 * "is waiting for DNS · needs A 203.0.113.7", or its error.
 */
function domainProblem(d: Domain) {
  if (d.dns?.status === "error") {
    return (
      <span className="text-warn">Cloudflare: {d.dns.reason ?? "error"}</span>
    );
  }
  if (d.status === "error") {
    return (
      <span className="text-warn">{d.status_reason ?? "has an error"}</span>
    );
  }
  const record = d.dns_records?.[0];
  return (
    <>
      is waiting for DNS
      {record && (
        <span className="text-mut">
          {" "}
          · needs{" "}
          <span className="font-mono">
            {record.type} {record.value}
          </span>
        </span>
      )}
    </>
  );
}

/**
 * One row of NeedsAttention: a coloured mark, what is wrong, and the
 * action at the end. VulnRow uses it too.
 */
export function Row({
  tone,
  action,
  children,
}: {
  tone: "fail" | "warn";
  action: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-soft py-2">
      <span
        aria-hidden
        className={`w-3 flex-none text-center ${tone === "fail" ? "text-fail" : "text-warn"}`}
      >
        {tone === "fail" ? "✕" : "▲"}
      </span>
      <p className="min-w-0 flex-1">{children}</p>
      <span className="flex flex-wrap items-center gap-2">{action}</span>
    </div>
  );
}
