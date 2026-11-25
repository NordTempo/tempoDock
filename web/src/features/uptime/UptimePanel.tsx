import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { listenPort, useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, clock, day, elapsed } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  MoreItem,
  MoreMenu,
  Scroll,
  StateText,
} from "../../ui/kit";
import { notify } from "../../ui/notice";
import { Saved } from "../create/form";
import {
  addCli,
  type NewUptimeCheck,
  type UptimeCheck,
  uptimeChecks,
  useAddUptimeCheck,
  useCheckDomains,
  useRemoveUptimeCheck,
  useUpdateUptimeCheck,
} from "./data";

// A service's uptime checks (ADR 0055), in Settings' Health and uptime
// section next to the health check they're easily confused with (ADR
// 0099): one row per check with its state, last day's strip and
// percentages, Add check, and the latest incidents. UptimeSummary is the
// one line Metrics and the overview show; HealthOnServer is Dock's own
// health check of the container (the view from inside, ADR 0059). Live
// state comes from uptime events (data.ts).

const intervals = [
  [30, "30s"],
  [60, "1m"],
  [300, "5m"],
  [900, "15m"],
  [3600, "1h"],
] as const;

/** An interval as the form offers it: "1m", else seconds. */
const every = (seconds: number) =>
  intervals.find(([s]) => s === seconds)?.[1] ?? `${seconds}s`;

/** 100% only for a perfect record; else at most 99.99%, as the CLI. */
function percent(p: number | undefined) {
  if (p === undefined) return "–";
  return p >= 100 ? "100%" : `${Math.min(p, 99.99).toFixed(2)}%`;
}

const stateWord = { up: "Up", down: "Down", unknown: "Unknown" };

/**
 * The uptime checks, under the health check rows in Settings: Add check
 * opens the form in place; Check all domains adds one per verified
 * domain without a check.
 */
export function UptimeChecks({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { domains } = useService(ref);
  const checks = useSuspenseQuery(uptimeChecks(ref)).data;
  const [adding, setAdding] = useState(false);
  // The check added here, marked in the list until the page changes.
  const [added, setAdded] = useState<number | null>(null);
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
    <div className="mt-5 flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h3 className="font-semibold">Uptime checks</h3>
        <span className="text-mut">
          Probes from outside; two failures in a row raise an alert.
        </span>
        {domains.length > 0 && (
          <span className="ml-auto flex flex-wrap items-center gap-2">
            {unchecked.length > 0 && (
              <CheckDomains
                serviceRef={ref}
                hosts={unchecked.map((d) => d.hostname)}
              />
            )}
            {!adding && (
              <Button onClick={() => setAdding(true)}>Add check</Button>
            )}
          </span>
        )}
      </div>
      <datalist id="uptime-statuses">
        <option value="200" />
        <option value="2xx" />
        <option value="3xx" />
      </datalist>
      {domains.length === 0 && (
        <p className="text-mut">
          Uptime checks probe one of the service's domains. Add a domain first.
        </p>
      )}
      {adding && (
        <AddCheck
          serviceRef={ref}
          host={domains[0]?.hostname ?? ""}
          checks={checks}
          onAdded={(id) => {
            setAdded(id);
            setAdding(false);
          }}
          onCancel={() => setAdding(false)}
        />
      )}
      {checks.length === 0 && domains.length > 0 && !adding && (
        <p className="text-mut">None yet.</p>
      )}
      {checks.length > 0 && (
        <Scroll>
          <table className="w-full">
            <thead>
              <tr>
                <th>State</th>
                <th>URL</th>
                <th>Every</th>
                <th>Expects</th>
                <th>Last 24 hours</th>
                <th className="text-right">24h</th>
                <th className="text-right">7d</th>
                <th className="text-right">30d</th>
                <th>Last probe</th>
                <th>From</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {checks.map((c) => (
                <CheckRow
                  key={c.id}
                  serviceRef={ref}
                  check={c}
                  fresh={c.id === added}
                />
              ))}
            </tbody>
          </table>
        </Scroll>
      )}
      {incidents.length > 0 && (
        <div className="flex flex-col gap-1">
          <h3 className="font-semibold">Last incidents</h3>
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
        </div>
      )}
    </div>
  );
}

/**
 * Uptime in one line, where it's watched (Metrics, the overview): up or
 * down, the last day's percentage and how many checks, with a link to
 * where they're set up (Settings › Health and uptime). Nothing when the
 * service has no checks and no domain to check.
 */
export function UptimeSummary({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const { domains } = useService(ref);
  const checks = useSuspenseQuery(uptimeChecks(ref)).data;
  if (checks.length === 0 && domains.length === 0) return null;
  const down = checks.filter((c) => c.state === "down");
  const known = checks.filter((c) => c.uptime_24h !== undefined);
  const worst = known.length
    ? Math.min(...known.map((c) => c.uptime_24h ?? 100))
    : undefined;
  return (
    <p className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
      {checks.length === 0 ? (
        <span className="text-mut">No uptime checks.</span>
      ) : (
        <span>
          {down.length > 0 ? (
            <span className="text-fail">
              {down.length === checks.length ? "down" : `${down.length} down`}
            </span>
          ) : checks.every((c) => c.state === "up") ? (
            <span className="text-ok">up</span>
          ) : (
            <span className="text-mut">waiting for the first probe</span>
          )}
          {worst !== undefined && ` · ${percent(worst)} in 24h`}
          {` · ${checks.length} ${checks.length === 1 ? "check" : "checks"}`}
        </span>
      )}
      <Link
        to="/$org/$project/$environment/$service/settings"
        params={{ org, ...ref }}
        hash="health"
        className="lnk"
      >
        {checks.length === 0 ? "Add one" : "Set up in Settings"}
      </Link>
    </p>
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
            className={`inline-block h-3.5 w-[3px] ${color}`}
            title={`${clock(b.at)}: ${b.total ? `${b.up} of ${b.total} up` : "no probes"}`}
          />
        );
      })}
    </span>
  );
}

/**
 * A URL as host and path: the host faint (cut first when space runs out),
 * the path in full. The scheme is left out unless it isn't https.
 */
function CheckUrl({ url }: { url: string }) {
  const u = new URL(url);
  const path = `${u.pathname}${u.search}`;
  return (
    <span className="inline-flex max-w-[220px] min-w-0 font-mono">
      <span className="min-w-0 truncate text-faint">
        {u.protocol === "https:" ? u.host : u.origin}
      </span>
      <span className="shrink-0">{path}</span>
    </span>
  );
}

/** A URL compared loosely: no trailing slash on the path, a lower-case host. */
function sameUrl(a: string, b: string) {
  const norm = (raw: string) => {
    try {
      const u = new URL(raw);
      return `${u.protocol}//${u.host}${u.pathname.replace(/\/+$/, "")}${u.search}`;
    } catch {
      return raw.trim();
    }
  };
  return norm(a) === norm(b);
}

function CheckRow({
  serviceRef: ref,
  check: c,
  fresh,
}: {
  serviceRef: ServiceRef;
  check: UptimeCheck;
  /** Just added here: marked, and says when the first probe runs. */
  fresh: boolean;
}) {
  const remove = useRemoveUptimeCheck(ref);
  const [confirming, setConfirming] = useState(false);
  const p = c.last_probe;
  return (
    <tr className={fresh ? "[&>td]:bg-sel" : ""}>
      <td>
        <StateText
          state={{
            tone: c.state === "up" ? "ok" : c.state === "down" ? "fail" : "mut",
            glyph: c.state === "up" ? "●" : c.state === "down" ? "✕" : "○",
            label: stateWord[c.state],
          }}
        />
      </td>
      <td
        className="whitespace-nowrap"
        title={`${c.url}, expects ${c.expect_status}`}
      >
        <CheckUrl url={c.url} />
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
      <td className="whitespace-nowrap">
        <IntervalEdit serviceRef={ref} check={c} />
      </td>
      <td className="whitespace-nowrap">
        <StatusEdit serviceRef={ref} check={c} />
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
          : `first probe within ${every(c.interval_seconds)}`}
      </td>
      <td className="text-mut">{c.server}</td>
      <td className="w-px py-1 text-right whitespace-nowrap">
        <MoreMenu label={`Check of ${c.url}`}>
          {(close) => (
            <MoreItem
              close={close}
              danger
              cli={`dock uptime remove ${c.id} -s ${ref.service} -e ${ref.environment}`}
              onClick={() => setConfirming(true)}
            >
              Remove check…
            </MoreItem>
          )}
        </MoreMenu>
        {confirming && (
          <Confirm
            danger
            action="Remove check"
            title={`Remove the check of ${c.url}?`}
            cli={`dock uptime remove ${c.id} -s ${ref.service} -e ${ref.environment}`}
            pending={remove.isPending}
            error={remove.error}
            onConfirm={() =>
              remove.mutate(c.id, { onSuccess: () => setConfirming(false) })
            }
            onCancel={() => {
              remove.reset();
              setConfirming(false);
            }}
          >
            Dock stops probing it, and its history goes with it.
          </Confirm>
        )}
      </td>
    </tr>
  );
}

/**
 * A check's interval as a select that saves on change, with Saved beside
 * it (the app's save model for selects).
 */
function IntervalEdit({
  serviceRef: ref,
  check: c,
}: {
  serviceRef: ServiceRef;
  check: UptimeCheck;
}) {
  const update = useUpdateUptimeCheck(ref);
  // An interval set from the CLI that the list doesn't offer stays choosable.
  const options: (readonly [number, string])[] = intervals.some(
    ([s]) => s === c.interval_seconds,
  )
    ? [...intervals]
    : [...intervals, [c.interval_seconds, `${c.interval_seconds}s`]];
  return (
    <span className="flex items-center gap-1.5">
      <select
        aria-label={`Interval of ${c.url}`}
        title={`dock uptime update ${c.id} --interval <d> -s ${ref.service} -e ${ref.environment}`}
        value={c.interval_seconds}
        disabled={update.isPending}
        onChange={(e) =>
          update.mutate({ id: c.id, interval_seconds: Number(e.target.value) })
        }
        className="field py-[3px] font-mono"
      >
        {options.map(([s, label]) => (
          <option key={s} value={s}>
            {label}
          </option>
        ))}
      </select>
      <Saved ok={update.isSuccess} />
      <ErrorText error={update.error} />
    </span>
  );
}

/**
 * A check's expected status, a text value: click to edit, Enter saves,
 * Esc cancels (the app's save model for text).
 */
function StatusEdit({
  serviceRef: ref,
  check: c,
}: {
  serviceRef: ServiceRef;
  check: UptimeCheck;
}) {
  const update = useUpdateUptimeCheck(ref);
  const [draft, setDraft] = useState<string | null>(null);
  const cancel = () => {
    update.reset();
    setDraft(null);
  };
  if (draft === null) {
    return (
      <span className="flex items-center gap-1.5">
        <button
          type="button"
          className="lnk font-mono"
          title={`Change the status that means up: dock uptime update ${c.id} --status <code> -s ${ref.service} -e ${ref.environment}`}
          onClick={() => setDraft(c.expect_status)}
        >
          {c.expect_status}
        </button>
        <Saved ok={update.isSuccess} />
      </span>
    );
  }
  const value = draft.trim();
  return (
    <form
      aria-label={`Expected status of ${c.url}`}
      className="flex items-center gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (value === c.expect_status) return cancel();
        update.mutate(
          { id: c.id, expect_status: value },
          { onSuccess: () => setDraft(null) },
        );
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          cancel();
        }
      }}
    >
      <input
        // biome-ignore lint/a11y/noAutofocus: opened by the value it replaces
        autoFocus
        aria-label="Expected status"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        list="uptime-statuses"
        className="field w-[64px] py-[3px] font-mono"
      />
      <button
        type="submit"
        className="btn sm pri"
        disabled={!value || update.isPending}
      >
        Save
      </button>
      <Button small onClick={cancel}>
        Cancel
      </Button>
      <ErrorText error={update.error} />
    </form>
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
export function HealthOnServer({
  serviceRef: ref,
}: {
  serviceRef: ServiceRef;
}) {
  const { service } = useService(ref);
  const { status } = service;
  const interval = service.health_check_interval_seconds;
  const health = status.health;
  return (
    <section
      aria-labelledby="uptime-inside"
      className="mt-5 flex flex-col gap-1"
    >
      <h3 id="uptime-inside" className="font-semibold">
        Health check on its server now
      </h3>
      {interval === -1 ? (
        <p className="text-mut">
          The ongoing health check is off; set an interval in Settings › Health
          and uptime.
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

/** A field's label above it, as in the invite form. */
const fieldLabel = "flex flex-col gap-1";

/**
 * One height for the add form's inputs, select and button, so labels and
 * bottoms line up (a native select is shorter than an input otherwise).
 */
const control = "h-[34px]";

/**
 * The add form. While the URL typed is one an existing check probes (any
 * status or interval), it says so before adding, and an exact repeat (same
 * URL, status and interval) can't be added. A new one gets a notice.
 */
function AddCheck({
  serviceRef: ref,
  host,
  checks,
  onAdded,
  onCancel,
}: {
  serviceRef: ServiceRef;
  host: string;
  checks: UptimeCheck[];
  onAdded: (id: number) => void;
  onCancel: () => void;
}) {
  const add = useAddUptimeCheck(ref);
  const [url, setUrl] = useState("");
  // The form opens from Add check, so its first field takes the focus.
  const urlField = useRef<HTMLInputElement>(null);
  useEffect(() => urlField.current?.focus(), []);
  const [status, setStatus] = useState("200");
  const [intervalS, setIntervalS] = useState(60);
  const [timeoutS, setTimeoutS] = useState(10);
  const req: NewUptimeCheck = {
    url: url.trim(),
    expect_status: status,
    interval_seconds: intervalS,
    timeout_seconds: timeoutS,
  };
  const existing =
    req.url === "" ? [] : checks.filter((c) => sameUrl(c.url, req.url));
  const repeat = existing.some(
    (c) =>
      c.expect_status === req.expect_status &&
      c.interval_seconds === req.interval_seconds,
  );
  // The check the warning offers to change instead of adding a second.
  const first = existing[0];
  const update = useUpdateUptimeCheck(ref);
  return (
    <form
      aria-label="Add an uptime check"
      className="flex flex-col gap-1.5"
      onKeyDown={(e) => e.key === "Escape" && onCancel()}
      onSubmit={(e) => {
        e.preventDefault();
        if (repeat) return;
        add.mutate(req, {
          onSuccess: (check) => {
            setUrl("");
            onAdded(check.id);
            notify(
              `Added a check of ${req.url}, every ${every(intervalS)}. The first probe runs within ${every(intervalS)}.`,
            );
          },
        });
      }}
    >
      {/* Labelled fields in a row, like the invite form: each says what
          it is, the status one what it means. */}
      <span className="flex flex-wrap items-end gap-2.5">
        <label className={fieldLabel}>
          <span className="text-xs text-sec">URL</span>
          <input
            ref={urlField}
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder={`https://${host}/`}
            className={`field w-[320px] font-mono ${control}`}
          />
        </label>
        <label className={fieldLabel} title="The answer that counts as up">
          <span className="text-xs text-sec">Up when it answers</span>
          <input
            value={status}
            onChange={(e) => setStatus(e.target.value.trim())}
            list="uptime-statuses"
            className={`field w-[96px] font-mono ${control}`}
          />
        </label>
        <label className={fieldLabel}>
          <span className="text-xs text-sec">Every</span>
          <select
            value={intervalS}
            onChange={(e) => setIntervalS(Number(e.target.value))}
            className={`field min-w-[96px] ${control}`}
          >
            {intervals.map(([s, label]) => (
              <option key={s} value={s}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <label className={fieldLabel}>
          <span className="text-xs text-sec">Timeout, seconds</span>
          <input
            type="number"
            min={1}
            max={30}
            value={timeoutS}
            onChange={(e) => setTimeoutS(Number(e.target.value))}
            className={`field w-[96px] font-mono ${control}`}
          />
        </label>
        {/* With a check of this URL already there, changing it is the
            primary (blind reviewers made duplicates otherwise); a second
            check stays possible. */}
        <Act
          primary={existing.length === 0}
          type="submit"
          className={control}
          cli={addCli(ref, req)}
          disabled={req.url === "" || repeat || add.isPending}
          reason={
            req.url === ""
              ? "type a URL first"
              : repeat
                ? "this exact check exists"
                : undefined
          }
        >
          {existing.length > 0 ? "Add a second check as well" : "Add check"}
        </Act>
        <Button className={control} onClick={onCancel}>
          Cancel
        </Button>
        <ErrorText error={add.error} />
      </span>
      {existing.length > 0 && (
        <span
          role="status"
          className="flex flex-wrap items-center gap-2 text-warn-text"
        >
          <span>
            ▲ Already checked:{" "}
            {existing
              .map(
                (c) =>
                  `every ${every(c.interval_seconds)}, expects ${c.expect_status}${c.auto ? " (added by Dock for the domain)" : ""}`,
              )
              .join("; ")}
            .
          </span>
          {first && !repeat && (
            <Button
              small
              primary
              disabled={update.isPending}
              title={`dock uptime update ${first.id} --interval ${intervalS}s --status ${status} -s ${ref.service} -e ${ref.environment}`}
              onClick={() =>
                update.mutate(
                  {
                    id: first.id,
                    interval_seconds: intervalS,
                    expect_status: status,
                  },
                  {
                    onSuccess: () => {
                      setUrl("");
                      notify(
                        `The check of ${first.url} now runs every ${every(intervalS)}, expecting ${status}.`,
                      );
                    },
                  },
                )
              }
            >
              Change the existing check to every {every(intervalS)}
              {status !== first.expect_status ? `, expects ${status}` : ""}
            </Button>
          )}
          <ErrorText error={update.error} />
        </span>
      )}
    </form>
  );
}
