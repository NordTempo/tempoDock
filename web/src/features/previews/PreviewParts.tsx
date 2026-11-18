import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { EnvRef } from "../../data/types";
import { ago } from "../../ui/format";
import { httpsHref } from "../../ui/httpsHref";
import { Confirm, StateText } from "../../ui/kit";
import { type State, shortId } from "../../ui/status";
import {
  type Preview,
  type PreviewDatabase,
  type PullRequestRef,
  previewDataText,
  previewQueries,
  previewsCli,
  useClosePreview,
  useRefreshPreviewData,
} from "./data";

// A preview environment's parts on the project overview (ADR 0056): its
// pull request, status and addresses, its databases' copies of the base
// (ADR 0092), and closing it.

export const previewStates: Record<Preview["status"], State> = {
  waiting: { tone: "mut", glyph: "○", label: "waiting" },
  deploying: { tone: "prog", glyph: "◐", label: "deploying" },
  live: { tone: "ok", glyph: "●", label: "live" },
  failed: { tone: "fail", glyph: "✕", label: "failed" },
  closed: { tone: "mut", glyph: "–", label: "closed" },
};

/** "PR #12 Fix checkout · alice", linking to the pull request on GitHub. */
export function PullRequestLink({ pr }: { pr: PullRequestRef }) {
  return (
    <a
      href={httpsHref(pr.url)}
      target="_blank"
      rel="noreferrer"
      title={`${pr.repo}#${pr.number}`}
      className="text-mut no-underline hover:text-fg"
    >
      <span className="font-mono text-info">PR #{pr.number}</span>{" "}
      <span className="text-sec">{pr.title}</span> · {pr.author}
    </a>
  );
}

/**
 * The preview's status, addresses and head, from the project's previews;
 * releases and domains of the environment refetch them (data/live.ts).
 */
export function PreviewLine({ env }: { env: EnvRef }) {
  const preview = useQuery({
    ...previewQueries.settings(env.project),
    select: (s) => s.previews.find((p) => p.environment === env.environment),
  }).data;
  if (!preview) return null;
  return (
    <p className="-mt-1 mb-2.5 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-xs text-mut">
      <StateText state={previewStates[preview.status]} />
      {preview.urls.map((u) => (
        <a
          key={u.service}
          href={u.url}
          target="_blank"
          rel="noreferrer"
          title={u.service}
          className="font-mono text-info hover:underline"
        >
          {u.url.replace(/^https?:\/\//, "")}
        </a>
      ))}
      <span className="font-mono">
        {preview.branch}
        {preview.commit && ` at ${shortId(preview.commit)}`}
      </span>
      {preview.fork && <span className="text-warn">fork</span>}
      {preview.event && <span className="text-faint">{preview.event}</span>}
    </p>
  );
}

/** A copy's numbers: "4 columns masked · 41 MB · 12 s · 2 hours ago". */
function copyFacts(d: PreviewDatabase) {
  const facts: string[] = [];
  if (d.masked_columns) facts.push(`${d.masked_columns} columns masked`);
  if (d.bytes)
    facts.push(
      d.bytes < 1 << 20
        ? "under 1 MB"
        : `${Math.round(d.bytes / (1 << 20))} MB`,
    );
  if (d.finished_at) {
    const secs = Math.round(
      (Date.parse(d.finished_at) - Date.parse(d.started_at)) / 1000,
    );
    facts.push(`${secs} s`, ago(d.finished_at));
  }
  return facts;
}

/**
 * One line per preview database that copies its base: how it went, and
 * Refresh from base for those who may deploy the preview. The copy's
 * end (a preview audit event) refetches the previews (data/live.ts).
 */
export function PreviewData({
  env,
  canDeploy,
}: {
  env: EnvRef;
  canDeploy: boolean;
}) {
  const [asking, setAsking] = useState(false);
  const refresh = useRefreshPreviewData();
  const dbs = useQuery({
    ...previewQueries.settings(env.project),
    select: (s) =>
      s.previews.find((p) => p.environment === env.environment)?.databases,
  }).data;
  if (!dbs || dbs.length === 0) return null;
  const copying = dbs.some((d) => d.status === "copying");
  return (
    <div className="-mt-1 mb-2.5 flex flex-col gap-0.5 text-xs">
      {dbs.map((d, i) => (
        <p key={d.service} className="flex flex-wrap items-baseline gap-x-1.5">
          <span className="w-10 text-mut">{i === 0 ? "Data" : ""}</span>
          <span className="font-mono">{d.service}</span>
          <span className="text-faint">·</span>
          {d.status === "copying" ? (
            <StateText
              state={{
                tone: "prog",
                glyph: "◐",
                label: `copying from ${d.from}`,
              }}
            />
          ) : d.status === "copied" ? (
            <span>
              {previewDataText(d.mode)} of{" "}
              <span className="font-mono">{d.from}</span>
            </span>
          ) : (
            <span className="text-warn">{d.message}</span>
          )}
          {d.status === "copying" && d.message && (
            <span className="text-faint">{d.message}</span>
          )}
          {d.status === "copied" &&
            copyFacts(d).map((f) => (
              <span key={f} className="text-mut">
                · {f}
              </span>
            ))}
          {canDeploy && i === 0 && !copying && (
            <button
              type="button"
              className="ml-2 text-info hover:underline"
              onClick={() => setAsking(true)}
            >
              Refresh from base
            </button>
          )}
        </p>
      ))}
      {asking && (
        <Confirm
          action={`Refresh ${env.environment} from base`}
          cli={previewsCli(`refresh ${env.environment}`, env.project)}
          pending={refresh.isPending}
          error={refresh.error}
          onConfirm={() =>
            refresh.mutate(env, { onSuccess: () => setAsking(false) })
          }
          onCancel={() => setAsking(false)}
        >
          Refresh <b className="font-semibold">{env.environment}</b>'s{" "}
          {dbs.map((d) => d.service).join(", ")} from{" "}
          {[...new Set(dbs.map((d) => d.from))].join(", ")}? Each is emptied and
          copied again as set, then the preview's services restart. Changes made
          in the preview are lost. The base is only read.
        </Confirm>
      )}
    </div>
  );
}

/**
 * Asks before closing a preview, as `dock environment previews close`: the
 * environment is archived, and its containers, volumes and addresses go.
 * The environment leaves the overview once closed.
 */
export function ClosePreview({
  env,
  onDone,
}: {
  env: EnvRef;
  onDone: () => void;
}) {
  const close = useClosePreview();
  return (
    <div className="mb-2.5">
      <Confirm
        danger
        action={`Close ${env.environment}`}
        cli={previewsCli(`close ${env.environment}`, env.project)}
        pending={close.isPending}
        error={close.error}
        onConfirm={() => close.mutate(env, { onSuccess: onDone })}
        onCancel={onDone}
      >
        Close preview <b className="font-semibold">{env.environment}</b>? Its
        containers, volumes, variables and addresses are removed. The pull
        request's next push makes a new one.
      </Confirm>
    </div>
  );
}
