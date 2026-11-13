import { useQuery } from "@tanstack/react-query";
import type { EnvRef } from "../../data/types";
import { Confirm, StateText } from "../../ui/kit";
import { type State, shortId } from "../../ui/status";
import {
  type Preview,
  type PullRequestRef,
  previewQueries,
  previewsCli,
  useClosePreview,
} from "./data";

// A preview environment's parts on the project overview (ADR 0056): its
// pull request, status and addresses, and closing it.

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
      href={pr.url}
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
