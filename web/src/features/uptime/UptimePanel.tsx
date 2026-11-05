import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { listenPort, useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { ago, clock, day, elapsed } from "../../ui/format";
import { Act, Button, ErrorText, Panel, SectionHead } from "../../ui/kit";
import {
  addCli,
  type NewUptimeCheck,
  type UptimeCheck,
  uptimeChecks,
  useAddUptimeCheck,
  useCheckDomains,
  useRemoveUptimeCheck,
} from "./data";

// A service's uptime checks (ADR 0055): the add form, one row per check
// with its state, last day's strip and percentages, then Dock's own health
// check of the container (the view from inside, ADR 0059) and the latest
// incidents. Live state comes from uptime events (data.ts).

const intervals = [
  [30, "30s"],
  [60, "1m"],
  [300, "5m"],
  [900, "15m"],
  [3600, "1h"],
] as const;

/** 100% only for a perfect record; else at most 99.99%, as the CLI. */
function percent(p: number | undefined) {
  if (p === undefined) return "–";
  return p >= 100 ? "100%" : `${Math.min(p, 99.99).toFixed(2)}%`;
}

const stateClass = { up: "text-ok", down: "text-fail", unknown: "text-mut" };
const stateWord = { up: "Up", down: "Down", unknown: "Unknown" };

export function UptimePanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { domains } = useService(ref);
  const checks = useSuspenseQuery(uptimeChecks(ref)).data;
  // Verified domains that serve the service and have no check yet.
  const checked = new Set(checks.map((c) => new URL(c.url).hostname));
  const unchecked = domains.filter(
    (d) => d.verified && !d.redirect_to && !checked.has(d.hostname),
  );
  const incidents = checks
    .flatMap((c) => c.incidents.map((i) => ({ ...i, url: c.url })))
    .sort((a, b) => b.opened_at.localeCompare(a.opened_at))
    .slice(0, 5);
  return (
    <>
      <section aria-labelledby="uptime-checks" className="flex flex-col gap-3">
        <SectionHead
          id="uptime-checks"
          aside="GET from another server; two failures in a row raise an alert"
          right={
            unchecked.length > 0 && (
              <CheckDomains
                serviceRef={ref}
                hosts={unchecked.map((d) => d.hostname)}
              />
            )
          }
        >
          From outside
        </SectionHead>
        {domains.length === 0 ? (
          <p className="text-mut">
            Uptime checks probe one of the service's domains. Add a domain
            first.
          </p>
        ) : (
          <AddCheck serviceRef={ref} host={domains[0]?.hostname ?? ""} />
        )}
        {checks.length > 0 && (
          <Panel>
            <table className="w-full">
              <thead>
                <tr>
                  <th>State</th>
                  <th>URL</th>
                  <th>Last 24 hours</th>
                  <th className="text-right">24h</th>
                  <th className="text-right">7d</th>
                  <th className="text-right">30d</th>
                  <th>Last probe</th>
                  <th>From</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {checks.map((c) => (
                  <CheckRow key={c.id} serviceRef={ref} check={c} />
                ))}
              </tbody>
            </table>
          </Panel>
        )}
      </section>
      <Inside serviceRef={ref} />
      {incidents.length > 0 && (
        <section aria-labelledby="uptime-incidents">
          <SectionHead id="uptime-incidents">Last incidents</SectionHead>
          <ul className="flex flex-col gap-1">
            {incidents.map((i) => (
              <li
                key={i.alert_id}
                className="flex flex-wrap gap-x-3 text-mut"
                title={i.message}
              >
                <span className="font-mono text-fg">
                  {day(i.opened_at)} {clock(i.opened_at)}
                </span>
                <span className={i.resolved_at ? "" : "text-fail"}>
                  {i.resolved_at
                    ? elapsed(
                        Date.parse(i.resolved_at) - Date.parse(i.opened_at),
                      )
                    : "ongoing"}
                </span>
                {checks.length > 1 && (
                  <span className="font-mono">{i.url}</span>
                )}
                <span className="min-w-0 flex-1 truncate">{i.message}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}

/** The last day in 48 half-hour cells: green, red, amber when mixed. */
function Strip({ series }: { series: UptimeCheck["series"] }) {
  return (
    <span className="flex gap-px" role="img" aria-label="Last 24 hours">
      {series.map((b) => {
        const color =
          b.total === 0
            ? "bg-line"
            : b.up === b.total
              ? "bg-ok"
              : b.up === 0
                ? "bg-fail"
                : "bg-warn";
        return (
          <span
            key={b.at}
            className={`inline-block h-3.5 w-[4px] ${color}`}
            title={`${clock(b.at)}: ${b.total ? `${b.up} of ${b.total} up` : "no probes"}`}
          />
        );
      })}
    </span>
  );
}

function CheckRow({
  serviceRef: ref,
  check: c,
}: {
  serviceRef: ServiceRef;
  check: UptimeCheck;
}) {
  const remove = useRemoveUptimeCheck(ref);
  const [confirming, setConfirming] = useState(false);
  const p = c.last_probe;
  return (
    <tr>
      <td className={stateClass[c.state]}>{stateWord[c.state]}</td>
      <td
        className="whitespace-nowrap"
        title={`${c.url}, expects ${c.expect_status}`}
      >
        <span className="inline-block max-w-[240px] truncate align-bottom font-mono">
          {c.url}
        </span>
        {c.auto && (
          <span
            className="text-faint"
            title="Added by Dock for a verified domain; goes with the domain"
          >
            {" "}
            auto
          </span>
        )}
      </td>
      <td>
        <Strip series={c.series} />
      </td>
      <td className="text-right font-mono">{percent(c.uptime_24h)}</td>
      <td className="text-right font-mono">{percent(c.uptime_7d)}</td>
      <td className="text-right font-mono">{percent(c.uptime_30d)}</td>
      <td
        className={`w-full min-w-[120px] whitespace-normal ${p && !p.up ? "text-fail" : "text-mut"}`}
      >
        {p
          ? `${p.up ? `${p.status} in ${p.latency_ms} ms` : p.error} · ${ago(p.at)}`
          : "not yet"}
      </td>
      <td className="text-mut">{c.server}</td>
      <td className="w-px py-1 whitespace-nowrap">
        {confirming ? (
          <span className="flex items-center gap-2">
            <Act
              small
              danger
              cli={`dock uptime remove ${c.id} -s ${ref.service} -e ${ref.environment}`}
              disabled={remove.isPending}
              onClick={() => remove.mutate(c.id)}
            >
              Remove
            </Act>
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

/** One click for the verified domains without a check. */
function CheckDomains({
  serviceRef: ref,
  hosts,
}: {
  serviceRef: ServiceRef;
  hosts: string[];
}) {
  const check = useCheckDomains(ref);
  return (
    <span className="flex items-center gap-2 text-xs">
      <span className="text-faint">
        {hosts.length === 1
          ? `${hosts[0]} has no check`
          : `${hosts.length} domains have no check`}
      </span>
      <Act
        small
        cli={`dock uptime domains -s ${ref.service} -e ${ref.environment}`}
        disabled={check.isPending}
        onClick={() => check.mutate()}
      >
        Check all domains
      </Act>
      <ErrorText error={check.error} />
    </span>
  );
}

/**
 * Dock's own health check of the container on its server: the view from
 * inside, next to the probes from outside. From the service's status, no
 * prober of its own.
 */
function Inside({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service } = useService(ref);
  const { status } = service;
  const interval = service.health_check_interval_seconds;
  const health = status.health;
  return (
    <section aria-labelledby="uptime-inside" className="flex flex-col">
      <SectionHead
        id="uptime-inside"
        aside="Dock's health check of the container, on its server"
      >
        From inside
      </SectionHead>
      {interval === -1 ? (
        <p className="text-mut">
          The ongoing health check is off; set an interval on Build &amp; run.
        </p>
      ) : (
        <p className="flex flex-wrap gap-x-3">
          <span
            className={
              health === "healthy"
                ? "text-ok"
                : health === "unhealthy"
                  ? "text-fail"
                  : "text-mut"
            }
          >
            {health === "healthy"
              ? "Healthy"
              : health === "unhealthy"
                ? "Unhealthy"
                : health === "starting"
                  ? "Starting"
                  : "Not running"}
          </span>
          <span className="font-mono">
            {service.health_check_path
              ? `GET :${listenPort(service)}${service.health_check_path}`
              : `TCP :${listenPort(service)}`}{" "}
            every {interval || 30} s
          </span>
          <span className="text-mut">
            {status.health_restarts
              ? `restarted ${status.health_restarts} ${status.health_restarts === 1 ? "time" : "times"} for failing checks`
              : "no restarts for failing checks"}
          </span>
        </p>
      )}
    </section>
  );
}

function AddCheck({
  serviceRef: ref,
  host,
}: {
  serviceRef: ServiceRef;
  host: string;
}) {
  const add = useAddUptimeCheck(ref);
  const [url, setUrl] = useState("");
  const [status, setStatus] = useState("200");
  const [intervalS, setIntervalS] = useState(60);
  const [timeoutS, setTimeoutS] = useState(10);
  const req: NewUptimeCheck = {
    url: url.trim(),
    expect_status: status,
    interval_seconds: intervalS,
    timeout_seconds: timeoutS,
  };
  return (
    <form
      aria-label="Add an uptime check"
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(req, { onSuccess: () => setUrl("") });
      }}
    >
      <input
        aria-label="URL"
        value={url}
        onChange={(e) => setUrl(e.target.value)}
        placeholder={`https://${host}/`}
        className="field w-[320px] py-[7px] font-mono"
      />
      <label className="flex items-center gap-1.5 text-mut">
        expects
        <input
          aria-label="Expected status"
          value={status}
          onChange={(e) => setStatus(e.target.value.trim())}
          list="uptime-statuses"
          className="field w-[64px] py-[7px] font-mono"
        />
        <datalist id="uptime-statuses">
          <option value="200" />
          <option value="2xx" />
          <option value="3xx" />
        </datalist>
      </label>
      <label className="flex items-center gap-1.5 text-mut">
        every
        <select
          aria-label="Interval"
          value={intervalS}
          onChange={(e) => setIntervalS(Number(e.target.value))}
          className="field py-[7px]"
        >
          {intervals.map(([s, label]) => (
            <option key={s} value={s}>
              {label}
            </option>
          ))}
        </select>
      </label>
      <label className="flex items-center gap-1.5 text-mut">
        timeout
        <input
          aria-label="Timeout in seconds"
          type="number"
          min={1}
          max={30}
          value={timeoutS}
          onChange={(e) => setTimeoutS(Number(e.target.value))}
          className="field w-[56px] py-[7px] font-mono"
        />
        s
      </label>
      <Act
        primary
        type="submit"
        cli={addCli(ref, req)}
        disabled={req.url === "" || add.isPending}
      >
        Add check
      </Act>
      <ErrorText error={add.error} />
    </form>
  );
}
