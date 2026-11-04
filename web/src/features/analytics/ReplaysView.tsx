import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { clock, day } from "../../ui/format";
import { Panel } from "../../ui/kit";
import {
  type Filter,
  type Replay,
  replayDimensions,
  type Site,
  type StatsRange,
  siteReplaysQuery,
} from "./data";
import { count, country } from "./parts";

// A site's session replays (ADR 0037, phase A3; Saile's pick: B). Each
// row shows the visit's shape: its page views and errors along its
// length, then where it went, who it was and what served it. A row opens
// the player, which walks this list with Previous and Next.

/** 4m 12s */
const length = (ms: number) => {
  const s = Math.max(0, Math.round(ms / 1000));
  return s < 60
    ? `${s}s`
    : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
};

const devices = [
  ["Any device", undefined],
  ["Desktop", "desktop"],
  ["Mobile", "mobile"],
  ["Tablet", "tablet"],
] as const;

/** The filters a replay list asks for: the usable shared ones and the device. */
export function replayFilters(
  filters: readonly Filter[],
  device?: string,
): Filter[] {
  const usable = filters.filter(
    (f) =>
      (replayDimensions as readonly string[]).includes(f.dimension) &&
      f.dimension !== "device",
  );
  return device
    ? [...usable, { dimension: "device", op: "is", value: device }]
    : usable;
}

export function ReplaysView({
  project,
  site,
  range,
  filters,
  errorsOnly,
  device,
  onErrorsOnly,
  onDevice,
}: {
  project: string;
  site: Site;
  range: StatsRange;
  filters: Filter[];
  errorsOnly: boolean;
  device?: string;
  onErrorsOnly: (on: boolean) => void;
  onDevice: (d: string | undefined) => void;
}) {
  const skipped = filters.filter(
    (f) => !(replayDimensions as readonly string[]).includes(f.dimension),
  );
  const { data, error, refetch } = useQuery(
    siteReplaysQuery(
      project,
      site.name,
      range,
      replayFilters(filters, device),
      errorsOnly,
    ),
  );
  const params = useParams({ strict: false });
  if (error) return <ErrorBlock error={error} onRetry={() => refetch()} />;
  if (!data) return <p className="text-mut">Loading…</p>;
  const replays = data.replays ?? [];
  const kept = site.settings.replay_retention_days;
  return (
    <div className="flex flex-col gap-3">
      {skipped.length > 0 && (
        <p className="text-xs text-mut">
          Replays don't record{" "}
          {skipped.map((f) => f.dimension.replace("_", " ")).join(", ")}; those
          filters are left out here.
        </p>
      )}
      {!data.recording && (
        <p className="text-warn-text">
          This site doesn't record visits. Turn on session replay in its
          settings; inputs are always masked.
        </p>
      )}
      {!data.store_online && (
        <p className="text-warn-text">{data.store_error}</p>
      )}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs">
        <span className="text-sm">
          <b className="font-semibold">{count(replays.length)}</b>{" "}
          {replays.length === 1 ? "replay" : "replays"}
        </span>
        <span className="text-faint">
          {data.recording
            ? `${data.sample_rate}% of page loads recorded · `
            : ""}
          kept {kept} days · inputs masked
        </span>
        <span className="flex gap-1">
          {(["All", "With errors"] as const).map((label) => (
            <button
              key={label}
              type="button"
              aria-pressed={(label === "With errors") === errorsOnly}
              className={`rounded-ctl px-1.5 ${(label === "With errors") === errorsOnly ? "bg-sel text-fg" : "text-mut"}`}
              onClick={() => onErrorsOnly(label === "With errors")}
            >
              {label}
            </button>
          ))}
        </span>
        <span className="flex gap-1">
          {devices.map(([label, d]) => (
            <button
              key={label}
              type="button"
              aria-pressed={device === d}
              className={`rounded-ctl px-1.5 ${device === d ? "bg-sel text-fg" : "text-mut"}`}
              onClick={() => onDevice(d)}
            >
              {label}
            </button>
          ))}
        </span>
        <span className="ml-auto flex items-center gap-2 text-faint">
          <span className="inline-block h-3 w-0.5 bg-faint" /> page view
          <span className="inline-block h-3 w-0.5 bg-fail" /> error
        </span>
      </div>
      <Panel>
        <table className="tabular-nums">
          <thead>
            <tr>
              <th className="w-24">Started</th>
              <th className="w-56">Session</th>
              <th>Pages and visitor</th>
            </tr>
          </thead>
          <tbody>
            {replays.map((r) => (
              <tr key={r.id}>
                <td className="text-xs">
                  <Link
                    to="/$org/$project/$environment/$service/analytics/replays/$replay"
                    params={{
                      org: params.org ?? "",
                      project,
                      environment: params.environment ?? "",
                      service: params.service ?? "",
                      replay: r.id,
                    }}
                    search={{
                      site: site.name,
                      ...(range !== "30d" && { range }),
                      ...(errorsOnly && { errors: true }),
                      ...(device && { device }),
                      ...(filters.length > 0 && {
                        f: filters.map(
                          (f) => `${f.dimension}:${f.op}:${f.value}`,
                        ),
                      }),
                    }}
                    className="block"
                  >
                    <span className="block text-fg">
                      {day(r.start)} {clock(r.start)}
                    </span>
                    <span className="text-mut">
                      {length(Date.parse(r.end) - Date.parse(r.start))}
                    </span>
                  </Link>
                </td>
                <td>
                  <Shape replay={r} />
                  <Summary replay={r} />
                </td>
                <td className="max-w-0 text-xs">
                  <span className="block truncate">
                    {r.entry_path} <span className="text-faint">→</span>{" "}
                    {r.exit_path}
                  </span>
                  <span className="block truncate text-mut">
                    {[
                      r.country && country(r.country),
                      r.device &&
                        r.device[0]?.toUpperCase() + r.device.slice(1),
                      r.browser,
                      r.os,
                      `${count(r.events)} ${r.events === 1 ? "event" : "events"}`,
                      r.release,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </td>
              </tr>
            ))}
            {replays.length === 0 && (
              <tr>
                <td colSpan={3} className="text-mut">
                  No replays in this range.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Panel>
    </div>
  );
}

/** The visit's page views and errors along its length. */
function Shape({ replay: r }: { replay: Replay }) {
  const start = Date.parse(r.start);
  const span = Math.max(1, Date.parse(r.end) - start);
  return (
    <span className="relative block h-3 w-52 rounded-full bg-line/60">
      {(r.marks ?? []).map((m, i) => (
        <span
          // Marks have no ID; the list is replaced whole.
          key={i}
          title={m.kind === "error" ? (m.name ?? "error") : "page view"}
          className={`absolute top-0 h-3 w-0.5 ${m.kind === "error" ? "bg-fail" : "bg-faint"}`}
          style={{
            left: `${Math.min(100, Math.max(0, ((m.t - start) / span) * 100))}%`,
          }}
        />
      ))}
    </span>
  );
}

/** "6 pages · TypeError, Error" */
function Summary({ replay: r }: { replay: Replay }) {
  const marks = r.marks ?? [];
  const pages = marks.filter((m) => m.kind === "pageview").length;
  const errors = [
    ...new Set(
      marks.filter((m) => m.kind === "error").map((m) => m.name || "error"),
    ),
  ];
  return (
    <span className="mt-0.5 block truncate text-xs text-mut">
      {pages || r.segments} {(pages || r.segments) === 1 ? "page" : "pages"}
      {errors.length > 0 && (
        <span className="text-fail"> · {errors.join(", ")}</span>
      )}
    </span>
  );
}
