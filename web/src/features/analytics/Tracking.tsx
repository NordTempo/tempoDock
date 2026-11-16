import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef } from "react";
import type { components } from "../../api/schema";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import { Act, CopyButton, ErrorText, StateText } from "../../ui/kit";
import type { State } from "../../ui/status";
import { type SiteTracking, siteTrackingQuery, useCheckSite } from "./data";

// Whether a site's tracking script works (plan item 5, mock A): a chip in
// the site bar and, when something is wrong, a checklist under it. The
// state comes from the last event; a site that isn't active gets its
// pages checked once when the page opens (check_due), and again with
// Check again. The live stream flips it to active (data.ts).

type Step = components["schemas"]["SiteCheckStep"];

/** The first failing step of a check, with its hostname. */
function firstProblem(t: SiteTracking): { host: string; step: Step } | null {
  for (const h of t.check?.hosts ?? []) {
    for (const step of h.steps ?? []) {
      if (step.result === "fail") return { host: h.hostname, step };
    }
  }
  return null;
}

/** The chip's state for a tracking answer. */
export function trackingState(t: SiteTracking, checking: boolean): State {
  const last = t.last_event_at ? ago(t.last_event_at) : "";
  const p = firstProblem(t);
  switch (t.state) {
    case "active":
      return { tone: "ok", glyph: "●", label: `active · ${last}` };
    case "waiting":
      return checking
        ? { tone: "mut", glyph: "◌", label: "checking the pages" }
        : { tone: "mut", glyph: "◌", label: "waiting for the first visit" };
    case "quiet":
      return { tone: "mut", glyph: "○", label: `quiet · ${last}` };
    case "stopped":
      return {
        tone: "fail",
        glyph: "●",
        label: `stopped · last event ${last}`,
      };
    case "no_script":
      return {
        tone: "warn",
        glyph: "▲",
        label: `no script on ${p?.host ?? "the page"}`,
      };
    case "wrong_host":
      return {
        tone: "warn",
        glyph: "▲",
        label: `wrong host: ${p?.host ?? "a hostname"}`,
      };
    case "blocked":
      return {
        tone: "warn",
        glyph: "▲",
        label: `blocked by ${p?.host ?? "the page"}'s CSP`,
      };
    default:
      return { tone: "mut", glyph: "○", label: "can't read analytics" };
  }
}

/** States that open the checklist by themselves. */
const problems: SiteTracking["state"][] = [
  "stopped",
  "no_script",
  "wrong_host",
  "blocked",
];

/**
 * The tracking chip: a button that opens the checklist. A site that isn't
 * active gets its pages checked once here when check_due.
 */
export function TrackingChip({
  project,
  site,
  open,
  onToggle,
}: {
  project: string;
  site: string;
  open: boolean;
  onToggle: () => void;
}) {
  const tracking = useQuery(siteTrackingQuery(project, site));
  const check = useCheckSite(project, site);
  const asked = useRef<string | null>(null);
  const t = tracking.data;
  useEffect(() => {
    if (t?.check_due && t.state !== "active" && asked.current !== site) {
      asked.current = site;
      check.mutate();
    }
  }, [t, site, check]);
  if (!t) return null;
  return (
    <button
      type="button"
      aria-expanded={open}
      title="Is the tracking script working? Click for the checklist"
      onClick={onToggle}
      className="inline-flex min-h-[24px] items-center"
    >
      <span className="sr-only">Tracking: </span>
      <StateText state={trackingState(t, check.isPending)} />
    </button>
  );
}

/** Whether the checklist starts open: when something is wrong. */
export const trackingOpens = (t: SiteTracking | undefined) =>
  !!t && problems.includes(t.state);

/**
 * The checklist under the bar: per hostname, what the check found, each
 * failure with its fix; Check again; the CLI equivalent.
 */
export function TrackingChecklist({
  project,
  site,
}: {
  project: string;
  site: string;
}) {
  const org = useOrg();
  const tracking = useQuery(siteTrackingQuery(project, site));
  const check = useCheckSite(project, site);
  const t = tracking.data;
  if (!t) return null;
  const mark = { ok: "✓", fail: "✗", skip: "–" } as const;
  const tone = {
    ok: "text-ok",
    fail: "text-fail",
    skip: "text-faint",
  } as const;
  return (
    <section
      aria-label="Tracking"
      className="max-w-[860px] border-line border-y py-2.5"
    >
      <div className="flex flex-wrap items-center gap-3">
        <span className="font-medium">Tracking</span>
        <span className="flex-1 text-mut text-xs">
          {t.last_event_at
            ? `last event ${ago(t.last_event_at)}`
            : "no events in 30 days"}
          {t.check && ` · checked ${ago(t.check.at)}`}
        </span>
        <Link
          to="/$org/analytics/$project/$site/settings"
          params={{ org, project, site }}
          hash="set-hostnames"
          className="text-xs"
        >
          Hostnames
        </Link>
        <Act
          small
          disabled={check.isPending}
          cli={`dock analytics check ${site} -p ${project}`}
          onClick={() => check.mutate()}
        >
          {check.isPending ? "Checking…" : "Check again"}
        </Act>
      </div>
      {t.store_error && (
        <p className="mt-1.5 text-warn text-xs">{t.store_error}</p>
      )}
      <ErrorText error={check.error} />
      {t.state === "active" && !t.check && (
        <p className="mt-1.5 text-sec">Events arrive: the script works.</p>
      )}
      {(t.check?.hosts ?? []).map((h) => (
        <div key={h.hostname} className="mt-2">
          <span className="font-mono text-sec text-xs">{h.hostname}</span>
          <ul>
            {(h.steps ?? []).map((st) => (
              <li
                key={st.text}
                className="grid grid-cols-[18px_minmax(0,1fr)_auto] items-baseline gap-2 border-soft border-b py-1.5"
              >
                <span className={`font-mono ${tone[st.result]}`}>
                  {mark[st.result]}
                </span>
                <span className={st.result === "fail" ? "text-fg" : "text-sec"}>
                  {st.text}
                  {st.fix && (
                    <span className="mt-0.5 block break-all font-mono text-fg text-xs">
                      {st.fix}
                    </span>
                  )}
                </span>
                {st.fix && <CopyButton text={st.fix} />}
              </li>
            ))}
          </ul>
        </div>
      ))}
      {t.check && (t.check.hosts ?? []).length === 0 && (
        <p className="mt-1.5 text-mut">
          The site has no hostname collecting; turn analytics on for one in its
          service's Domains.
        </p>
      )}
    </section>
  );
}

/**
 * The tracking state on its own, for the site's Settings page (its Script
 * row); the chip on the dashboard runs the checks.
 */
export function TrackingStatus({
  project,
  site,
}: {
  project: string;
  site: string;
}) {
  const t = useQuery(siteTrackingQuery(project, site)).data;
  if (!t) return null;
  return <StateText state={trackingState(t, false)} />;
}
