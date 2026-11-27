import { Link } from "@tanstack/react-router";
import { useChanges } from "../../data/queries";
import type { EnvRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, plural } from "../../ui/format";
import { shortSha } from "../git/api";
import type { ComposeSource } from "./data";

const outcomeTone = {
  staged: "text-warn",
  applied: "text-ok",
  unchanged: "text-mut",
  failed: "text-fail",
} as const;

/**
 * What a followed Compose file is and how its last sync went (ADR 0108):
 * the repository, branch and path, the commit, the outcome in one line,
 * and the environment's staged changes with a link to review them.
 * onReview runs when that link is followed (a dialog closes).
 */
export function SyncOutcome({
  env,
  source,
  onReview,
}: {
  env: EnvRef;
  source: ComposeSource;
  onReview?: () => void;
}) {
  const org = useOrg();
  const changes = useChanges(env);
  const last = source.last;
  return (
    <div className="flex flex-col gap-2.5">
      <div className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-0.5">
        <span className="text-mut">follows</span>
        <span className="font-mono [overflow-wrap:anywhere]">
          {source.repo} <span className="text-mut">@</span> {source.branch}{" "}
          <span className="text-mut">:</span> {source.path}
        </span>
        {last && (
          <>
            <span className="text-mut">last sync</span>
            <span className="[overflow-wrap:anywhere]">
              <span className="font-mono">{shortSha(last.commit.sha)}</span>{" "}
              {last.commit.message}
              <span className="text-mut">
                {last.commit.author && ` · ${last.commit.author}`} ·{" "}
                {ago(last.at)}
              </span>
            </span>
            <span className="text-mut">outcome</span>
            <span className={outcomeTone[last.outcome]}>
              {last.outcome}: {last.summary}
            </span>
          </>
        )}
        {source.pending && (
          <>
            <span className="text-mut">queued</span>
            <span className="font-mono">{shortSha(source.pending)}</span>
          </>
        )}
        <span className="text-mut">removals</span>
        <span>
          {source.prune ? (
            <>
              staged when a service leaves the file{" "}
              <span className="text-mut">(applying them needs an admin)</span>
            </>
          ) : (
            <span className="text-mut">kept: services the file drops stay</span>
          )}
        </span>
        <span className="text-mut">auto-apply</span>
        <span className="text-mut">
          {source.auto_apply
            ? "applies a sync that removes nothing, when nothing else waits"
            : "off: every sync waits for review"}
        </span>
      </div>
      {changes.count > 0 && (
        <p className="flex flex-wrap items-center gap-x-3.5 gap-y-1 rounded-ctl border border-warn-edge bg-stage-bg px-3 py-2 text-warn-text">
          {plural(changes.count, "change")} waiting to apply in{" "}
          {env.environment}
          <Link
            to="/$org/$project/settings/variables/$environment"
            params={{ org, project: env.project, environment: env.environment }}
            className="lnk"
            onClick={onReview}
          >
            Review and apply
          </Link>
        </p>
      )}
    </div>
  );
}
