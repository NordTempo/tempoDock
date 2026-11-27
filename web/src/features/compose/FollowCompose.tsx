import { useState } from "react";
import type { EnvRef } from "../../data/types";
import { Dialog } from "../../ui/Dialog";
import { plural } from "../../ui/format";
import { Act, Button, Check, ErrorText } from "../../ui/kit";
import { Section, Warn } from "../apply/ComposeParts";
import { composeRows } from "../apply/composePlan";
import { Rows } from "../apply/PlanView";
import { Field, Fields, input } from "../create/form";
import { useGitHubRepos } from "../git/api";
import { type ComposeSource, useFollowCompose } from "./data";
import { SyncOutcome } from "./SyncOutcome";

/** The most warnings the dialog lists. */
const maxWarnings = 50;

/**
 * Follow a Compose file in GitHub (ADR 0108): the repository (one the
 * org's GitHub App reads), branch and path, whether removals are staged
 * and whether syncs apply themselves. Following syncs the branch's head
 * at once, and the dialog then shows that sync's outcome and plan. With
 * `source`, it edits what is followed. Loaded on first open.
 */
export default function FollowCompose({
  env,
  source,
  onClose,
}: {
  env: EnvRef;
  source: ComposeSource | null;
  onClose: () => void;
}) {
  const repos = useGitHubRepos(true);
  const [repo, setRepo] = useState(source?.repo ?? "");
  const [branch, setBranch] = useState(source?.branch ?? "");
  const [path, setPath] = useState(source?.path ?? "");
  const [prune, setPrune] = useState(source?.prune ?? false);
  const [autoApply, setAutoApply] = useState(source?.auto_apply ?? false);
  const follow = useFollowCompose(env);
  const done = follow.data;
  const flags = [
    branch.trim() && ` --branch ${branch.trim()}`,
    path.trim() && ` --path ${path.trim()}`,
    prune && " --prune",
    autoApply && " --auto-apply",
  ]
    .filter(Boolean)
    .join("");
  const cli = `dock compose follow ${repo.trim() || "OWNER/REPO"} -p ${env.project} -e ${env.environment}${flags}`;
  const changes = composeRows(done?.plan.changes ?? [], "changes");
  const manual = composeRows(done?.plan.manual ?? [], "manual");

  return (
    <Dialog title="Follow a Compose file" onClose={onClose}>
      <div className="flex flex-col gap-3.5">
        <Fields>
          <Field label="Repository">
            <input
              value={repo}
              onChange={(e) => setRepo(e.target.value)}
              list="compose-repos"
              placeholder="owner/name, read through the GitHub App"
              spellCheck={false}
              autoComplete="off"
              disabled={!!done}
              className={input}
            />
          </Field>
          <datalist id="compose-repos">
            {(repos.data?.repos ?? []).map((r) => (
              <option key={r.id} value={r.full_name} />
            ))}
          </datalist>
          <Field label="Branch">
            <input
              value={branch}
              onChange={(e) => setBranch(e.target.value)}
              placeholder="the repository's default"
              spellCheck={false}
              autoComplete="off"
              disabled={!!done}
              className={input}
            />
          </Field>
          <Field label="Path">
            <input
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="docker-compose.yml or compose.yaml at the root"
              spellCheck={false}
              autoComplete="off"
              disabled={!!done}
              className={input}
            />
          </Field>
        </Fields>
        <Check
          label={
            <>
              stage archives for services the file drops{" "}
              <span className="text-mut">
                (admins only; applying them still needs an admin)
              </span>
            </>
          }
          checked={prune}
          onChange={setPrune}
          disabled={!!done}
        />
        <Check
          label="apply a sync by itself when it removes nothing, nothing else is staged here, and no secret needs a value"
          checked={autoApply}
          onChange={setAutoApply}
          disabled={!!done}
        />
        <div className="flex flex-wrap items-center gap-2">
          <Act
            primary
            cli={cli}
            disabled={!repo.trim() || follow.isPending || !!done}
            onClick={() =>
              follow.mutate({
                repo: repo.trim(),
                branch: branch.trim() || undefined,
                path: path.trim() || undefined,
                prune,
                auto_apply: autoApply,
              })
            }
          >
            {source ? "Save and sync" : "Follow and sync"}
          </Act>
          <Button onClick={onClose}>{done ? "Done" : "Cancel"}</Button>
          <ErrorText error={follow.error} />
        </div>
        {done && (
          <div className="flex max-h-[50vh] flex-col gap-3 overflow-y-auto border-t border-line pt-3">
            <SyncOutcome env={env} source={done.source} onReview={onClose} />
            {(done.warnings ?? []).length > 0 && (
              <Section title="Not mapped exactly">
                {(done.warnings ?? []).slice(0, maxWarnings).map((w, i) => (
                  <Warn key={`${w.service}:${w.key}:${i}`}>
                    {w.service && (
                      <span className="font-mono text-faint">{w.service} </span>
                    )}
                    {w.message}
                  </Warn>
                ))}
                {(done.warnings ?? []).length > maxWarnings && (
                  <p className="text-mut">
                    and {(done.warnings ?? []).length - maxWarnings} more;{" "}
                    <code className="font-mono">dock compose sync</code> prints
                    them all
                  </p>
                )}
              </Section>
            )}
            {changes.length > 0 && (
              <Rows title={plural(changes.length, "change")} rows={changes} />
            )}
            {manual.length > 0 && <Rows title="Needs you" rows={manual} />}
          </div>
        )}
      </div>
    </Dialog>
  );
}
