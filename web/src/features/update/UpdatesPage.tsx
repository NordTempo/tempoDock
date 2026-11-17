import type { ReactNode } from "react";
import { ago, day, plural } from "../../ui/format";
import {
  Act,
  ErrorText,
  Glyph,
  Page,
  PageBar,
  Panel,
  SectionHead,
} from "../../ui/kit";
import type { GlyphChar, Tone } from "../../ui/status";
import { toneText } from "../../ui/status";
import {
  type UpdateServer,
  type UpdateStatus,
  useCheckNow,
  useStartUpdate,
  useUpdateStatus,
} from "./data";
import { headlines, Inline, Notes } from "./Notes";

// The console's Updates page (ADR 0028, mock A): the newer releases' notes
// beside what an update does to each server, in order. While an update
// runs its steps replace the plan; a failed or rolled-back update stays on
// top with the end of its log. Update events keep it current.

/** The install guide's section on updating by hand. */
const UPGRADING =
  "https://github.com/NordTempo/tempoDock/blob/main/docs/guide/install.md#upgrading";

/** How many releases are listed; older ones are a count. */
const NOTES_SHOWN = 10;

/** How many of the newest releases open with their headlines. */
const NOTES_OPEN = 3;

export function UpdatesPage() {
  const st = useUpdateStatus();
  const check = useCheckNow();
  const start = useStartUpdate();
  const behind = (st.servers ?? []).filter((s) => s.behind && !s.manual);
  const running = st.running;
  let action = null;
  if (running) {
    action = (
      <Act cli="dock update" disabled>
        Updating…
      </Act>
    );
  } else if (st.available && st.latest && st.updater) {
    action = (
      <Act
        primary
        cli="dock update apply"
        disabled={start.isPending}
        onClick={() => start.mutate(st.latest)}
      >
        {st.last?.to === st.latest && st.last.status !== "succeeded"
          ? `Try ${st.latest} again`
          : `Update to ${st.latest}`}
      </Act>
    );
  } else if (!st.available && behind.length > 0) {
    action = (
      <Act
        primary
        cli="dock update apply"
        disabled={start.isPending}
        onClick={() => start.mutate(st.version)}
      >
        Update {plural(behind.length, "server")}
      </Act>
    );
  }
  return (
    <>
      <PageBar
        title="Updates"
        actions={
          <>
            <Act
              cli="dock update"
              disabled={check.isPending}
              onClick={() => check.mutate()}
            >
              {check.isPending ? "Checking…" : "Check now"}
            </Act>
            {action}
          </>
        }
      />
      <Page>
        {/* Status in the body, not beside the title. */}
        <p className="text-sec">
          Runs <span className="font-mono text-fg">{st.version}</span>
          {st.checked_at && <>, releases checked {ago(st.checked_at)}</>}.
        </p>
        <ErrorText error={start.error ?? check.error} />
        <Problems st={st} />
        {st.last && st.last.status !== "succeeded" && !running && (
          <LastFailed last={st.last} />
        )}
        {running ? (
          <Progress st={st} />
        ) : (
          <div className="grid gap-8 lg:grid-cols-2">
            <ReleaseNotes st={st} />
            <Plan st={st} />
          </div>
        )}
      </Page>
    </>
  );
}

/**
 * The release check's error: what failed as one sentence, and the fix on
 * a second line, linked to the install guide when it is the token. The
 * daemon's text is "Couldn't check for releases: what; how to fix it."
 * (the technical detail stays in the title and the log). The header's
 * Check now tries again.
 */
function CheckError({ error, detail }: { error: string; detail?: string }) {
  const [what = error, ...rest] = error.split(/;\s*/);
  const fix = rest.join("; ").replace(/\.$/, "");
  return (
    <div role="alert" title={detail} className="flex flex-col gap-1">
      <p className="text-fail">
        <Glyph char="✕" /> {what.replace(/\.$/, "")}.
      </p>
      {fix && (
        <p className="text-sec">
          {fix.charAt(0).toUpperCase()}
          {fix.slice(1)}.{" "}
          {/token/i.test(fix) && (
            <a href={UPGRADING} target="_blank" rel="noreferrer">
              How to add the token
            </a>
          )}
        </p>
      )}
    </div>
  );
}

/** Why the check or an update can't happen, and what to do. */
function Problems({ st }: { st: UpdateStatus }) {
  return (
    <>
      {st.check_error && (
        <CheckError error={st.check_error} detail={st.check_detail} />
      )}
      {!st.updater && (
        <p className="text-warn-text">
          <span className="text-warn">
            <Glyph char="▲" />
          </span>{" "}
          No updater on this control plane. Update it once by hand with the new
          release's install.sh (
          <a
            href={UPGRADING}
            target="_blank"
            rel="noopener noreferrer"
            className="text-fg underline underline-offset-3"
          >
            Upgrading
          </a>
          ); later updates run from here.
        </p>
      )}
    </>
  );
}

function LastFailed({ last }: { last: NonNullable<UpdateStatus["last"]> }) {
  const rolledBack = last.status === "rolled_back";
  return (
    <section aria-labelledby="last-update">
      <SectionHead
        id="last-update"
        aside={`${ago(last.finished_at)}${last.requested_by ? ` · by ${last.requested_by}` : ""}`}
      >
        <span className="text-fail">
          <Glyph char="✕" />
        </span>{" "}
        {rolledBack
          ? `Rolled back to ${last.from}`
          : `Updating to ${last.to} failed`}
      </SectionHead>
      <Panel>
        <div className="flex flex-col gap-1 px-3 py-2.5">
          <p>{last.error}</p>
          <p className="text-mut">
            {rolledBack &&
              "The binary, units and database are back as they were; changes made while the new version ran are lost. "}
            On the server:{" "}
            <span className="font-mono">journalctl -u dock-update -u dock</span>
          </p>
        </div>
        {last.log && (
          <pre className="max-h-64 overflow-auto border-t border-line bg-side px-3 py-2.5 font-mono text-2xs whitespace-pre-wrap text-sec">
            {last.log.trim().split("\n").slice(-40).join("\n")}
          </pre>
        )}
      </Panel>
    </section>
  );
}

/**
 * The newer releases, newest first: the newest few open with each
 * section's headlines, the rest one line each that opens the same way.
 * The full notes are behind a disclosure and on the release's page.
 */
function ReleaseNotes({ st }: { st: UpdateStatus }) {
  const releases = st.releases ?? [];
  const shown = releases.slice(0, NOTES_SHOWN);
  const more = releases.length - shown.length;
  // The source's list of releases, from a release's own page.
  const all = releases[0]?.url?.replace(/\/tag\/[^/]+$/, "");
  return (
    <section aria-labelledby="notes" className="min-w-0">
      <SectionHead
        id="notes"
        aside={
          releases.length > 0
            ? `${plural(releases.length, "release")} newer than ${st.version}`
            : undefined
        }
      >
        Release notes
      </SectionHead>
      {shown.length === 0 ? (
        <p className="text-mut">
          {st.check_error
            ? "Unknown until the check works."
            : st.latest
              ? `${st.latest} is the newest release.`
              : "No releases found yet."}
          {st.last?.status === "succeeded" &&
            ` Last update: ${st.last.from} to ${st.last.to}, ${ago(st.last.finished_at)}.`}
        </p>
      ) : (
        <div className="border-t border-line">
          {shown.map((r, i) => (
            <Release key={r.version} release={r} open={i < NOTES_OPEN} />
          ))}
          {more > 0 && (
            <p className="py-2 text-xs text-faint">
              {plural(more, "older release")} not shown.{" "}
              {all && (
                <a
                  href={all}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-info"
                >
                  All releases
                </a>
              )}
            </p>
          )}
        </div>
      )}
    </section>
  );
}

/**
 * One release: version and date, then its headlines (one line while
 * closed, a row per section when open), the full notes and its page.
 */
function Release({
  release: r,
  open,
}: {
  release: NonNullable<UpdateStatus["releases"]>[number];
  open: boolean;
}) {
  const sections = headlines(r.notes);
  // Closed, upgrade steps are noise; open, they are their own row.
  const line = sections
    .filter((s) => !/^upgrading$/i.test(s.title))
    .flatMap((s) => s.lines)
    .join(" · ");
  return (
    <details open={open} className="group border-b border-soft py-2">
      <summary className="flex cursor-pointer list-none items-baseline gap-2 [&::-webkit-details-marker]:hidden">
        <span aria-hidden className="w-2 text-mut group-open:rotate-90">
          ›
        </span>
        <span className="font-mono text-sm font-semibold">{r.version}</span>
        <span className="text-2xs whitespace-nowrap text-faint">
          {day(r.published_at)}
          {r.prerelease && " · pre-release"}
        </span>
        <span className="min-w-0 flex-1 truncate text-xs text-sec group-open:hidden">
          <Inline text={line} plain />
        </span>
      </summary>
      <div className="flex flex-col gap-2 pt-2 pl-4 text-xs">
        {sections.length > 0 ? (
          <dl className="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)] gap-x-3 gap-y-1.5">
            {sections.map((s, i) => (
              <div key={i} className="contents">
                <dt className="text-mut">{s.title}</dt>
                <dd className="text-sec">
                  <Inline text={s.lines.join(" · ")} />
                </dd>
              </div>
            ))}
          </dl>
        ) : (
          <p className="text-faint">No notes.</p>
        )}
        <div className="flex items-baseline gap-3">
          {r.notes.trim() && (
            <details className="group/full min-w-0 flex-1">
              <summary className="flex w-fit cursor-pointer list-none items-center gap-1.5 text-mut hover:text-fg [&::-webkit-details-marker]:hidden">
                <span aria-hidden className="group-open/full:rotate-90">
                  ›
                </span>
                Full notes
              </summary>
              <Notes text={r.notes} />
            </details>
          )}
          {r.url && (
            <a
              href={r.url}
              target="_blank"
              rel="noopener noreferrer"
              className="ml-auto text-info"
            >
              Release page
            </a>
          )}
        </div>
      </div>
    </details>
  );
}

/** What an update does to each server, in order. */
function Plan({ st }: { st: UpdateStatus }) {
  const target = st.available && st.latest ? st.latest : st.version;
  const selfUpdate = target !== st.version;
  const cp = (st.servers ?? []).find((s) => s.local);
  const nodes = (st.servers ?? []).filter((s) => !s.local);
  // A failed check can't tell whether anything is behind.
  const current = st.check_error ? (
    <span className="text-mut">unknown</span>
  ) : (
    <span className="text-mut">up to date</span>
  );
  let n = 1;
  return (
    <section aria-labelledby="plan" className="min-w-0">
      <SectionHead id="plan">
        {selfUpdate ? "What happens" : "Servers"}
      </SectionHead>
      <Panel>
        <table>
          <thead>
            <tr>
              {selfUpdate && <th>#</th>}
              <th>Server</th>
              <th>{selfUpdate ? "Now" : "Version"}</th>
              <th className="w-full">{selfUpdate ? "Then" : ""}</th>
            </tr>
          </thead>
          <tbody className="[&_td]:whitespace-normal">
            {selfUpdate && (
              <>
                <tr>
                  <td className="py-2 text-faint">{n++}</td>
                  <td colSpan={3} className="py-2">
                    Download {target} and check it against its SHA256SUMS
                  </td>
                </tr>
                <tr>
                  <td className="py-2 text-faint">{n++}</td>
                  <td colSpan={3} className="py-2">
                    Back up the control plane
                  </td>
                </tr>
              </>
            )}
            {cp && (
              <tr>
                {selfUpdate && <td className="py-2 text-faint">{n++}</td>}
                <td className="py-2 font-mono">
                  {cp.name}{" "}
                  <span className="font-sans text-xs whitespace-nowrap text-mut">
                    control plane
                  </span>
                </td>
                <td className="py-2 font-mono">{cp.version}</td>
                <td className="py-2">
                  {selfUpdate ? (
                    <>
                      <span className="font-mono">{target}</span>{" "}
                      <span className="text-faint">
                        · API down a few seconds
                      </span>
                    </>
                  ) : (
                    current
                  )}
                </td>
              </tr>
            )}
            {nodes.map((s) => (
              <NodeRow
                key={s.name}
                s={s}
                target={target}
                step={selfUpdate ? n++ : null}
                current={current}
              />
            ))}
          </tbody>
        </table>
      </Panel>
      {selfUpdate && (
        <p className="mt-2.5 text-xs text-mut">
          Apps keep running throughout. A server that isn't healthy within 90
          seconds goes back to {st.version}; the control plane with its database
          from the backup.
        </p>
      )}
    </section>
  );
}

function NodeRow({
  s,
  target,
  step,
  current,
}: {
  s: UpdateServer;
  target: string;
  step: number | null;
  /** What a server on the target shows: up to date, or unknown. */
  current: ReactNode;
}) {
  const offline = s.status !== "online";
  let then = current;
  if (s.manual) {
    then = (
      <span className="text-warn">
        can't update over its link:{" "}
        <span className="font-mono">{s.manual}</span>
      </span>
    );
  } else if (offline && (s.behind || s.version !== target)) {
    then = <span className="text-fail">{s.status}, skipped</span>;
  } else if (s.behind || s.version !== target) {
    then = (
      <>
        <span className="font-mono">{target}</span>{" "}
        <span className="text-faint">· over its link</span>
      </>
    );
  }
  return (
    <tr>
      {step !== null && (
        <td className="py-2 text-faint">{offline || s.manual ? "·" : step}</td>
      )}
      <td className="py-2 font-mono">{s.name}</td>
      <td className="py-2 font-mono">{s.version || "·"}</td>
      <td className="py-2">{then}</td>
    </tr>
  );
}

type Step = { key: string; glyph: GlyphChar; tone: Tone; text: string };

/** The steps of the running update, done, current and to come. */
function Progress({ st }: { st: UpdateStatus }) {
  const run = st.running;
  if (!run) return null;
  const stage = run.stage;
  const cpDone = stage === "servers" || st.version === run.version;
  const steps: Step[] = [
    {
      key: "download",
      ...(stage === "downloading"
        ? current("Downloading and checking against SHA256SUMS")
        : done(`Downloaded and verified ${run.version}`)),
    },
    {
      key: "cp",
      ...(cpDone
        ? done(`Control plane on ${run.version}, healthy`)
        : stage === "applying"
          ? current(
              "Backing up and updating the control plane; the console reconnects when it is back",
            )
          : waiting("Back up and update the control plane")),
    },
  ];
  for (const s of (st.servers ?? []).filter((s) => !s.local)) {
    const label = s.name;
    switch (s.state) {
      case "updating":
        steps.push({
          key: label,
          ...current(`${label}: updating over its link`),
        });
        break;
      case "updated":
        steps.push({ key: label, ...done(`${label} on ${run.version}`) });
        break;
      case "failed":
        steps.push({
          key: label,
          glyph: "✕",
          tone: "fail",
          text: `${label}: ${s.error ?? "failed"}`,
        });
        break;
      case "skipped":
        steps.push({
          key: label,
          glyph: "–",
          tone: "mut",
          text: `${label}: ${s.error ?? "skipped"}`,
        });
        break;
      default:
        steps.push({
          key: label,
          ...(s.version === run.version
            ? done(`${label} on ${run.version}`)
            : waiting(`${label}: after the control plane`)),
        });
    }
  }
  return (
    <section aria-labelledby="progress">
      <SectionHead
        id="progress"
        aside={`started ${ago(run.started_at)}${run.requested_by ? ` by ${run.requested_by}` : ""}`}
      >
        Updating to {run.version}
      </SectionHead>
      <Panel>
        <ul className="flex flex-col px-3">
          {steps.map((s) => (
            <li
              key={s.key}
              className="flex gap-2.5 border-b border-soft py-2 last:border-0"
            >
              <span className={`w-3.5 text-center ${toneText[s.tone]}`}>
                <Glyph char={s.glyph} />
              </span>
              <span className={s.tone === "mut" ? "text-mut" : ""}>
                {s.text}
              </span>
            </li>
          ))}
        </ul>
      </Panel>
      <p className="mt-2.5 text-xs text-mut">
        Apps keep running. While the control plane restarts the console loses
        the API for a few seconds and reconnects on its own.
      </p>
    </section>
  );
}

const done = (text: string) => ({
  glyph: "✓" as const,
  tone: "ok" as const,
  text,
});
const current = (text: string) => ({
  glyph: "◐" as const,
  tone: "prog" as const,
  text,
});
const waiting = (text: string) => ({
  glyph: "○" as const,
  tone: "mut" as const,
  text,
});
