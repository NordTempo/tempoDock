import { ErrorBlock } from "../../ui/ErrorBlock";
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
import { Notes } from "./Notes";

// The console's Updates page (ADR 0028, mock A): the newer releases' notes
// beside what an update does to each server, in order. While an update
// runs its steps replace the plan; a failed or rolled-back update stays on
// top with the end of its log. Update events keep it current.

/** How many releases' notes are shown; older ones are a count. */
const NOTES_SHOWN = 8;

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
        after={
          <span className="text-xs text-faint">
            runs <span className="font-mono">{st.version}</span>
            {st.checked_at && <> · checked {ago(st.checked_at)}</>} ·{" "}
            <button
              type="button"
              className="text-info hover:text-fg"
              disabled={check.isPending}
              onClick={() => check.mutate()}
            >
              {check.isPending ? "Checking…" : "Check now"}
            </button>
          </span>
        }
        actions={action}
      />
      <Page>
        <ErrorText error={start.error ?? check.error} />
        <Problems st={st} onCheck={() => check.mutate()} />
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

/** Why the check or an update can't happen, and what to do. */
function Problems({
  st,
  onCheck,
}: {
  st: UpdateStatus;
  /** Checks for releases again. */
  onCheck: () => void;
}) {
  return (
    <>
      {st.check_error && (
        <ErrorBlock
          error={new Error(`Could not check ${st.source}: ${st.check_error}`)}
          onRetry={onCheck}
        />
      )}
      {!st.updater && (
        <p className="text-warn">
          <Glyph char="▲" /> This control plane has no updater
          (dock-update.path). Update it by hand once, with install.sh from the
          new release (docs/guide/install.md#upgrading); later updates run from
          here.
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

function ReleaseNotes({ st }: { st: UpdateStatus }) {
  const shown = (st.releases ?? []).slice(0, NOTES_SHOWN);
  const more = (st.releases ?? []).length - shown.length;
  return (
    <section aria-labelledby="notes" className="min-w-0">
      <SectionHead
        id="notes"
        aside={
          (st.releases ?? []).length > 0
            ? `${plural((st.releases ?? []).length, "release")} newer than ${st.version}`
            : undefined
        }
      >
        Release notes
      </SectionHead>
      {shown.length === 0 ? (
        <p className="text-mut">
          {st.latest
            ? `${st.latest} is the newest release.`
            : "No releases found yet."}
          {st.last?.status === "succeeded" &&
            ` Last update: ${st.last.from} to ${st.last.to}, ${ago(st.last.finished_at)}.`}
        </p>
      ) : (
        <div className="flex flex-col gap-4">
          {shown.map((r) => (
            <article key={r.version}>
              <h3 className="text-sm font-semibold">
                <span className="font-mono">{r.version}</span>{" "}
                <span className="text-2xs font-normal text-faint">
                  · {day(r.published_at)}
                  {r.prerelease && " · pre-release"}
                  {r.url && (
                    <>
                      {" · "}
                      <a
                        href={r.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-info"
                      >
                        GitHub
                      </a>
                    </>
                  )}
                </span>
              </h3>
              {r.notes.trim() ? (
                <Notes text={r.notes} />
              ) : (
                <p className="text-xs text-faint">No notes.</p>
              )}
            </article>
          ))}
          {more > 0 && (
            <p className="text-xs text-faint">
              {plural(more, "older release")} not shown.
            </p>
          )}
        </div>
      )}
    </section>
  );
}

/** What an update does to each server, in order. */
function Plan({ st }: { st: UpdateStatus }) {
  const target = st.available && st.latest ? st.latest : st.version;
  const selfUpdate = target !== st.version;
  const cp = (st.servers ?? []).find((s) => s.local);
  const nodes = (st.servers ?? []).filter((s) => !s.local);
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
                <td className="py-2 font-mono whitespace-nowrap">
                  {cp.name} <span className="text-faint">control plane</span>
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
                    <span className="text-mut">up to date</span>
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
}: {
  s: UpdateServer;
  target: string;
  step: number | null;
}) {
  const offline = s.status !== "online";
  let then = <span className="text-mut">up to date</span>;
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
